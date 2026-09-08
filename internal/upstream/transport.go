package upstream

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/andybalholm/brotli"
	utls "github.com/refraction-networking/utls"
)

// Transport performs requests to the CommandCode API over a raw
// HTTP/1.1 connection whose TLS handshake and request line/header order
// mirror the official CLI's Node fetch (undici) behavior. It implements
// http.RoundTripper so it can be plugged into the standard http.Client.
//
// Why hand-rolled: Go's crypto/tls produces a ClientHello (JA3/JA4) that
// differs from Node's OpenSSL 3 one, and net/http serializes request
// headers in alphabetical order while undici sends them in code/insertion
// order. Both are trivially detectable by the server.
type Transport struct {
	// DialTimeout bounds TCP connect + TLS handshake. Default 15s.
	DialTimeout time.Duration
	// HandshakeTimeout bounds only the TLS handshake. Default 15s.
	HandshakeTimeout time.Duration
	// InsecureSkipVerify is only meant for the local tap server in tests.
	InsecureSkipVerify bool

	mu     sync.Mutex
	pooled map[string]*pooledConn
}

type pooledConn struct {
	addr string
	conn net.Conn
	br   *bufio.Reader
	next *pooledConn
}

// New returns a Transport with an empty connection pool.
func New() *Transport {
	return &Transport{pooled: map[string]*pooledConn{}}
}

func (t *Transport) timeouts() (time.Duration, time.Duration) {
	d := t.DialTimeout
	if d <= 0 {
		d = 15 * time.Second
	}
	h := t.HandshakeTimeout
	if h <= 0 {
		h = 15 * time.Second
	}
	return d, h
}

// RoundTrip issues req and returns the response. The
// returned response body, when fully drained without error, puts the
// underlying connection back into the pool; on any error the connection
// is closed.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return nil, fmt.Errorf("upstream: only https is supported, got %q", req.URL.Scheme)
	}
	body, err := readBody(req)
	if err != nil {
		return nil, err
	}
	addr := hostPort(req.URL.Host)

	// Try a pooled connection first. A POST has server-side effects, so a
	// retry is only safe when the request provably never reached the
	// server (stale keep-alive detected before/at write time).
	for attempt := 0; attempt < 2; attempt++ {
		pc, fresh, err := t.acquire(req.Context(), addr)
		if err != nil {
			return nil, err
		}
		resp, err := t.exchange(req, pc, body, fresh)
		if err == nil {
			return resp, nil
		}
		pc.Close()
		if !errors.Is(err, errRequestNotSent) || attempt == 1 {
			return nil, err
		}
		// stale pooled connection: retry once with a fresh dial
	}
	return nil, errors.New("upstream: unreachable")
}

// errRequestNotSent marks failures that happened before the server could
// have seen the request, making a retry safe.
var errRequestNotSent = errors.New("request not sent")

func hostPort(host string) string {
	if strings.Contains(host, ":") {
		return host
	}
	return host + ":443"
}

func readBody(req *http.Request) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	defer req.Body.Close()
	if req.GetBody != nil {
		b, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		return io.ReadAll(b)
	}
	return io.ReadAll(req.Body)
}

// acquire returns a usable connection, reusing a pooled one when possible.
// The bool result reports whether the connection was freshly dialed.
func (t *Transport) acquire(ctx context.Context, addr string) (*pooledConn, bool, error) {
	t.mu.Lock()
	pc := t.pooled[addr]
	if pc != nil {
		t.pooled[addr] = pc.next
		pc.next = nil
	}
	t.mu.Unlock()
	if pc != nil {
		return pc, false, nil
	}

	dialTO, hsTO := t.timeouts()
	d := &net.Dialer{Timeout: dialTO}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, true, fmt.Errorf("upstream: dial %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		raw.SetDeadline(dl)
	} else {
		raw.SetDeadline(time.Now().Add(dialTO + hsTO))
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	spec, err := Spec(host)
	if err != nil {
		raw.Close()
		return nil, true, err
	}
	cfg := &utls.Config{
		ServerName:         host,
		InsecureSkipVerify: t.InsecureSkipVerify,
	}
	uconn := utls.UClient(raw, cfg, utls.HelloCustom)
	if err := uconn.ApplyPreset(spec); err != nil {
		raw.Close()
		return nil, true, fmt.Errorf("upstream: apply fingerprint: %w", err)
	}
	// HandshakeContext honors the dial deadline already set above; clear
	// deadlines before handing the connection to the per-request reader.
	if err := uconn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, true, fmt.Errorf("upstream: tls handshake: %w", err)
	}
	if np := uconn.ConnectionState().NegotiatedProtocol; np != "" && np != "http/1.1" {
		raw.Close()
		return nil, true, fmt.Errorf("upstream: unexpected negotiated protocol %q", np)
	}
	pc = &pooledConn{addr: addr, conn: uconn, br: bufio.NewReaderSize(uconn, 32*1024)}
	return pc, true, nil
}

// writeRequest serializes the request exactly as the CLI's Node 24 undici
// does on the wire. The header set and order are endpoint-specific (each
// CLI call site builds its own header object) — captured via tools/tap from
// the real command-code 1.50.1:
//
//	/alpha/generate           content-type, User-Agent, x-command-code-version,
//	                          x-cli-environment, x-project-slug, x-taste-learning,
//	                          x-session-id, Authorization, traceparent
//	/alpha/lifecycle-events   content-type, x-cli-environment, Authorization,
//	                          User-Agent, x-command-code-version
//	/alpha/fingerprint/record content-type, Authorization, x-cli-environment,
//	                          x-command-code-version, User-Agent
//
// content-type arrives lowercase and its value is doubled ("application/json,
// application/json") on every endpoint except fingerprint, because the CLI
// sets it both in its header bag and via the fetch body option and undici
// joins the two. net/http would sort headers alphabetically with Go casing —
// that ordering difference is itself a fingerprint.
func writeRequest(w io.Writer, req *http.Request, body []byte) error {
	var b strings.Builder
	b.WriteString(req.Method)
	b.WriteString(" ")
	b.WriteString(req.URL.RequestURI())
	b.WriteString(" HTTP/1.1\r\n")

	wire := func(name, value string) {
		b.WriteString(name)
		b.WriteString(": ")
		b.WriteString(value)
		b.WriteString("\r\n")
	}
	h := req.Header
	wireIf := func(wireName, headerName string) {
		if v := h.Get(headerName); v != "" {
			wire(wireName, v)
		}
	}

	wire("host", req.URL.Host)
	wire("connection", "keep-alive")

	ct := h.Get("Content-Type")
	ctDoubled := ct
	if ct == "application/json" {
		ctDoubled = "application/json, application/json"
	}

	switch req.URL.Path {
	case "/alpha/generate":
		wire("content-type", ctDoubled)
		wireIf("User-Agent", "User-Agent")
		wireIf("x-command-code-version", "X-Command-Code-Version")
		wireIf("x-cli-environment", "X-Cli-Environment")
		wireIf("x-project-slug", "X-Project-Slug")
		wireIf("x-taste-learning", "X-Taste-Learning")
		wireIf("x-session-id", "X-Session-Id")
		wireIf("Authorization", "Authorization")
		wire("traceparent", newTraceparent())
	default: // lifecycle / fingerprint / whoami share one shape family
		if req.URL.Path == "/alpha/fingerprint/record" {
			wire("content-type", ct)
			wireIf("Authorization", "Authorization")
			wireIf("x-cli-environment", "X-Cli-Environment")
			wireIf("x-command-code-version", "X-Command-Code-Version")
			wireIf("User-Agent", "User-Agent")
		} else {
			wire("content-type", ctDoubled)
			wireIf("x-cli-environment", "X-Cli-Environment")
			wireIf("Authorization", "Authorization")
			wireIf("User-Agent", "User-Agent")
			wireIf("x-command-code-version", "X-Command-Code-Version")
		}
	}

	wire("accept", "*/*")
	wire("accept-language", "*")
	wire("sec-fetch-mode", "cors")
	wire("accept-encoding", "br, gzip, deflate")
	if len(body) > 0 || req.Method == http.MethodPost {
		fmt.Fprintf(&b, "content-length: %d\r\n", len(body))
	}
	b.WriteString("\r\n")

	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}
	if len(body) > 0 {
		if _, err := w.Write(body); err != nil {
			return err
		}
	}
	return nil
}

// newTraceparent emits a W3C trace context header the CLI's chat span
// generator produces: "00-<32 hex trace id>-<16 hex span id>-01".
func newTraceparent() string {
	var t [16]byte
	var s [8]byte
	if _, err := crand.Read(t[:]); err != nil {
		panic(err)
	}
	if _, err := crand.Read(s[:]); err != nil {
		panic(err)
	}
	return "00-" + hex.EncodeToString(t[:]) + "-" + hex.EncodeToString(s[:]) + "-01"
}

func (t *Transport) exchange(req *http.Request, pc *pooledConn, body []byte, fresh bool) (*http.Response, error) {
	ctx := req.Context()
	_, hsTO := t.timeouts()
	if dl, ok := ctx.Deadline(); ok {
		pc.conn.SetDeadline(dl)
	} else {
		pc.conn.SetDeadline(time.Now().Add(hsTO))
	}
	if err := writeRequest(pc.conn, req, body); err != nil {
		return nil, fmt.Errorf("upstream: write request: %w: %w", errRequestNotSent, err)
	}
	resp, err := http.ReadResponse(pc.br, req)
	if err != nil {
		// A stale pooled keep-alive can fail the response read with no
		// bytes received; the write may have gone into a dead socket
		// buffer, so the server never saw the request — safe to retry.
		if !fresh && pc.br.Buffered() == 0 {
			return nil, fmt.Errorf("upstream: read response on pooled conn: %w: %w", errRequestNotSent, err)
		}
		return nil, fmt.Errorf("upstream: read response: %w", err)
	}
	resp.Body = &bodyCloser{
		pc:     pc,
		tr:     t,
		ctx:    ctx,
		raw:    decodeBody(resp),
		closed: false,
	}
	return resp, nil
}

func decodeBody(resp *http.Response) io.Reader {
	switch enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))); enc {
	case "gzip":
		r, err := gzip.NewReader(resp.Body)
		if err != nil {
			resp.Body.Close()
			return errReader{err}
		}
		return r
	case "br":
		return brotli.NewReader(resp.Body)
	case "deflate":
		// Node advertises "deflate" but servers sometimes emit raw deflate.
		if r, err := zlib.NewReader(resp.Body); err == nil {
			return r
		}
		return flate.NewReader(resp.Body)
	default:
		return resp.Body
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// bodyCloser streams the decoded response body; on clean EOF it releases
// the connection to the pool, on error or early Close it discards it.
type bodyCloser struct {
	pc     *pooledConn
	tr     *Transport
	ctx    context.Context
	raw    io.Reader
	closed bool
	done   bool
	mu     sync.Mutex
}

func (b *bodyCloser) Read(p []byte) (int, error) {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return 0, http.ErrBodyReadAfterClose
	}
	// Refresh the deadline for streaming SSE responses.
	if dl, ok := b.ctx.Deadline(); ok {
		b.pc.conn.SetDeadline(dl)
	} else {
		b.pc.conn.SetDeadline(time.Now().Add(5 * time.Minute))
	}
	n, err := b.raw.Read(p)
	if err != nil {
		b.finish(err == io.EOF)
	}
	return n, err
}

func (b *bodyCloser) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.finish(false)
	return nil
}

func (b *bodyCloser) finish(clean bool) {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return
	}
	b.done = true
	b.mu.Unlock()
	if clean {
		b.tr.release(b.pc)
	} else {
		b.pc.Close()
	}
}

func (t *Transport) release(pc *pooledConn) {
	// Only reusable if the peer kept the connection open (keep-alive).
	t.mu.Lock()
	defer t.mu.Unlock()
	if pc.next != nil {
		return
	}
	if t.pooled == nil {
		t.pooled = map[string]*pooledConn{}
	}
	pc.conn.SetDeadline(time.Now().Add(55 * time.Second)) // idle expiry
	pc.next = t.pooled[pc.addr]
	t.pooled[pc.addr] = pc
}

// CloseIdleConnections implements the optional interface used by
// http.Client; the proxy does not call it, but tests may.
func (t *Transport) CloseIdleConnections() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for addr, head := range t.pooled {
		for pc := head; pc != nil; pc = pc.next {
			pc.Close()
		}
		delete(t.pooled, addr)
	}
}

func (pc *pooledConn) Close() error {
	if pc.conn != nil {
		return pc.conn.Close()
	}
	return nil
}
