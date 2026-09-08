package upstream

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
)

// ---- local tap: peek raw ClientHello, then serve keep-alive as TLS ----

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "tap.local"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

type tap struct {
	addr      string
	helloCh   chan []byte
	requestCh chan string
	accepts   int32
}

func startTap(t *testing.T, enc, respBody string) *tap {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tp := &tap{
		addr:      ln.Addr().String(),
		helloCh:   make(chan []byte, 4),
		requestCh: make(chan string, 8),
	}
	cert := selfSignedCert(t)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go tp.handle(conn, cert, enc, respBody)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return tp
}

// bufConn reads through a bufio.Reader (so peeked bytes are not lost)
// while writes bypass the buffer.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func encodeBody(enc, body string) (extraHeader string, payload []byte) {
	switch enc {
	case "gzip":
		var buf bytes.Buffer
		w := gzip.NewWriter(&buf)
		w.Write([]byte(body))
		w.Close()
		return "content-encoding: gzip\r\n", buf.Bytes()
	case "br":
		var buf bytes.Buffer
		w := brotli.NewWriter(&buf)
		w.Write([]byte(body))
		w.Close()
		return "content-encoding: br\r\n", buf.Bytes()
	default:
		return "", []byte(body)
	}
}

func (tp *tap) handle(conn net.Conn, cert tls.Certificate, enc, respBody string) {
	defer conn.Close()
	atomic.AddInt32(&tp.accepts, 1)
	br := bufio.NewReader(conn)
	// Peek the first TLS record = the ClientHello, before TLS takes over.
	head, err := br.Peek(5)
	if err != nil {
		return
	}
	recLen := int(binary.BigEndian.Uint16(head[3:5]))
	for br.Buffered() < 5+recLen {
		if _, err := br.Peek(1); err != nil {
			return
		}
	}
	hello, err := br.Peek(5 + recLen)
	if err != nil {
		return
	}
	cp := make([]byte, len(hello))
	copy(cp, hello)

	tc := tls.Server(&bufConn{Conn: conn, r: br}, &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"http/1.1"},
	})
	if err := tc.Handshake(); err != nil {
		return
	}
	tp.helloCh <- cp

	rbr := bufio.NewReader(tc)
	for {
		var req bytes.Buffer
		var bodyLen int
		for {
			line, err := rbr.ReadString('\n')
			req.WriteString(line)
			if err != nil {
				return
			}
			if line == "\r\n" {
				break
			}
			if n, ok := strings.CutPrefix(strings.ToLower(line), "content-length:"); ok {
				fmt.Sscanf(strings.TrimSpace(n), "%d", &bodyLen)
			}
		}
		// Drain the request body so the graceful close below carries no
		// unread receive data (which would make the peer send an RST).
		if bodyLen > 0 {
			io.CopyN(io.Discard, rbr, int64(bodyLen))
		}
		hdr, payload := encodeBody(enc, respBody)
		fmt.Fprintf(tc, "HTTP/1.1 200 OK\r\ncontent-type: application/json\r\n%scontent-length: %d\r\n\r\n", hdr, len(payload))
		if _, err := tc.Write(payload); err != nil {
			return
		}
		tp.requestCh <- req.String()
	}
}

// ---- ClientHello structural parser (JA3 inputs) ----

type helloFields struct {
	versions     []uint16
	ciphers      []uint16
	compressions []uint8
	extTypes     []uint16
	extData      map[uint16][]byte
}

func parseClientHello(t *testing.T, rec []byte) helloFields {
	t.Helper()
	if len(rec) < 5 || rec[0] != 0x16 {
		t.Fatalf("not a handshake record: % x", rec[:min(len(rec), 8)])
	}
	msg := rec[5:]
	if msg[0] != 0x01 {
		t.Fatalf("not a client hello: %02x", msg[0])
	}
	// handshake header (4) + version(2) + random(32)
	p := msg[4+2+32:]
	sidLen := int(p[0])
	p = p[1+sidLen:]
	csLen := int(binary.BigEndian.Uint16(p))
	p = p[2:]
	var f helloFields
	for i := 0; i < csLen; i += 2 {
		f.ciphers = append(f.ciphers, binary.BigEndian.Uint16(p[i:i+2]))
	}
	p = p[csLen:]
	cmLen := int(p[0])
	p = p[1:]
	f.compressions = append(f.compressions, p[:cmLen]...)
	p = p[cmLen:]
	extTotal := int(binary.BigEndian.Uint16(p))
	p = p[2:]
	f.extData = map[uint16][]byte{}
	for extTotal >= 4 {
		typ := binary.BigEndian.Uint16(p)
		l := int(binary.BigEndian.Uint16(p[2:4]))
		if extTotal < 4+l {
			t.Fatalf("extension %d body overruns block", typ)
		}
		body := p[4 : 4+l]
		f.extTypes = append(f.extTypes, typ)
		b := make([]byte, len(body))
		copy(b, body)
		f.extData[typ] = b
		extTotal -= 4 + l
		p = p[4+l:]
	}
	// supported_versions (0x002b): body = 1-byte list length + versions
	if v, ok := f.extData[0x2b]; ok {
		l := int(v[0])
		for i := 1; i < 1+l; i += 2 {
			f.versions = append(f.versions, binary.BigEndian.Uint16(v[i:i+2]))
		}
	}
	return f
}

// ---- tests ----

func TestRoundTripMatchesNodeBaseline(t *testing.T) {
	// Baseline: the exact bytes a real Node v22 fetch sent to the tap,
	// captured earlier and stored next to this test.
	baseline, err := os.ReadFile("node24_clienthello.bin")
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	want := parseClientHello(t, baseline)

	tp := startTap(t, "", `{"ok":true}`)

	cl := &http.Client{
		Transport: &Transport{InsecureSkipVerify: true},
		Timeout:   10 * time.Second,
	}
	// Use a hostname, not the IP literal: RFC 6066 (and Node/OpenSSL and
	// utls alike) forbids SNI for IP addresses, so the extension would be
	// correctly absent and defeat the SNI assertion below.
	host := "localhost:" + tp.addr[strings.LastIndexByte(tp.addr, ':')+1:]
	body := strings.NewReader(`{"probe":1}`)
	req, err := http.NewRequest("POST", "https://"+host+"/alpha/generate", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cli")
	req.Header.Set("x-command-code-version", "1.50.1")
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("x-project-slug", "demo-proj")
	req.Header.Set("x-taste-learning", "true")
	req.Header.Set("x-session-id", "5178a72b-04a7-497d-8639-8c87974f3288")
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != `{"ok":true}` {
		t.Fatalf("body %q", got)
	}

	gotHello := <-tp.helloCh
	gotReq := <-tp.requestCh

	replay := parseClientHello(t, gotHello)
	if len(replay.ciphers) != len(want.ciphers) {
		t.Fatalf("cipher count %d want %d", len(replay.ciphers), len(want.ciphers))
	}
	for i := range want.ciphers {
		if replay.ciphers[i] != want.ciphers[i] {
			t.Errorf("cipher[%d]=%04x want %04x", i, replay.ciphers[i], want.ciphers[i])
		}
	}
	if fmt.Sprint(replay.extTypes) != fmt.Sprint(want.extTypes) {
		t.Errorf("extension order:\n got %v\nwant %v", replay.extTypes, want.extTypes)
	}
	// supported_versions (0x002b)
	if fmt.Sprint(replay.versions) != fmt.Sprint(want.versions) {
		t.Errorf("supported_versions:\n got %v\nwant %v", replay.versions, want.versions)
	}
	// supported_groups (0x000a): curve list must match
	if g, w := hex.EncodeToString(replay.extData[0x0a]), hex.EncodeToString(want.extData[0x0a]); g != w {
		t.Errorf("supported_groups mismatch:\n got %s\nwant %s", g, w)
	}
	// signature_algorithms (0x000d)
	if g, w := hex.EncodeToString(replay.extData[0x0d]), hex.EncodeToString(want.extData[0x0d]); g != w {
		t.Errorf("signature_algorithms mismatch:\n got %s\nwant %s", g, w)
	}
	// ALPN (0x0010)
	if g, w := hex.EncodeToString(replay.extData[0x10]), hex.EncodeToString(want.extData[0x10]); g != w {
		t.Errorf("alpn mismatch:\n got %x\nwant %x", g, w)
	}
	// SNI (0x0000) must be present and carry the dialed hostname.
	if v, ok := replay.extData[0x00]; !ok {
		t.Error("SNI extension missing in replayed hello")
	} else if !bytes.Contains(v, []byte("localhost")) {
		t.Errorf("SNI extension does not carry localhost: % x", v)
	}

	// HTTP/1.1 request line + header order must match the Node capture.
	wantLines := []string{
		"POST /alpha/generate HTTP/1.1",
		"host: " + host,
		"connection: keep-alive",
		"content-type: application/json, application/json",
		"User-Agent: cli",
		"x-command-code-version: 1.50.1",
		"x-cli-environment: production",
		"x-project-slug: demo-proj",
		"x-taste-learning: true",
		"x-session-id: 5178a72b-04a7-497d-8639-8c87974f3288",
		"Authorization: Bearer sk-test",
		"TRACEPARENT",
		"accept: */*",
		"accept-language: *",
		"sec-fetch-mode: cors",
		"accept-encoding: br, gzip, deflate",
		"content-length: 11",
	}
	gotLines := strings.Split(strings.TrimRight(gotReq, "\r\n"), "\r\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("header line count %d want %d:\n%q", len(gotLines), len(wantLines), gotReq)
	}
	for i := range wantLines {
		if wantLines[i] == "TRACEPARENT" {
			// W3C trace context is random per call; assert shape only.
			if !traceparentRE.MatchString(gotLines[i]) {
				t.Errorf("line %d: %q is not a traceparent", i, gotLines[i])
			}
			continue
		}
		if gotLines[i] != wantLines[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, gotLines[i], wantLines[i])
		}
	}
}

var traceparentRE = regexp.MustCompile(`^traceparent: 00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)

func TestLifecycleFingerprintWhoamiTemplates(t *testing.T) {
	tp := startTap(t, "", `{"ok":true}`)
	cl := &http.Client{
		Transport: &Transport{InsecureSkipVerify: true},
		Timeout:   10 * time.Second,
	}
	// Full expected wire lines (after the request line + host + connection)
	// for each beacon endpoint, taken verbatim from the tools/tap capture.
	// whoami is a GET with no body yet still carries the doubled content-type
	// the CLI's header object sets — that is real undici behaviour, confirmed
	// against the authoritative 1.50.1 capture.
	const tail = "accept: */*\r\n" +
		"accept-language: *\r\n" +
		"sec-fetch-mode: cors\r\n" +
		"accept-encoding: br, gzip, deflate\r\n"

	type headCase struct {
		method, path string
		withBody     bool
		want         string // full request text after host+connection lines
	}
	cases := []headCase{
		{
			method: "GET", path: "/alpha/whoami", withBody: false,
			want: "content-type: application/json, application/json\r\n" +
				"x-cli-environment: production\r\n" +
				"Authorization: Bearer sk-test\r\n" +
				"User-Agent: cli\r\n" +
				"x-command-code-version: 1.50.1\r\n" + tail,
		},
		{
			method: "POST", path: "/alpha/lifecycle-events", withBody: true,
			want: "content-type: application/json, application/json\r\n" +
				"x-cli-environment: production\r\n" +
				"Authorization: Bearer sk-test\r\n" +
				"User-Agent: cli\r\n" +
				"x-command-code-version: 1.50.1\r\n" + tail +
				"content-length: 2\r\n",
		},
		{
			method: "POST", path: "/alpha/fingerprint/record", withBody: true,
			want: "content-type: application/json\r\n" +
				"Authorization: Bearer sk-test\r\n" +
				"x-cli-environment: production\r\n" +
				"x-command-code-version: 1.50.1\r\n" +
				"User-Agent: cli\r\n" + tail +
				"content-length: 2\r\n",
		},
	}
	for _, c := range cases {
		var reqBody io.Reader
		if c.withBody {
			reqBody = strings.NewReader(`{}`)
		}
		req, _ := http.NewRequest(c.method, "https://"+tp.addr+c.path, reqBody)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "cli")
		req.Header.Set("Authorization", "Bearer sk-test")
		req.Header.Set("x-cli-environment", "production")
		req.Header.Set("x-command-code-version", "1.50.1")
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
		got := <-tp.requestCh
		// Compare everything after "host:" and "connection: keep-alive" —
		// those two differ per test run (ephemeral port) and are asserted in
		// the generate template test.
		lines := strings.Split(got, "\r\n")
		if len(lines) < 1 || lines[0] != c.method+" "+c.path+" HTTP/1.1" {
			t.Fatalf("%s request line: %q", c.path, lines[0])
		}
		if !strings.HasPrefix(lines[1], "host:") || lines[2] != "connection: keep-alive" {
			t.Fatalf("%s head lines: %q", c.path, lines[:3])
		}
		gotRest := strings.Join(lines[3:], "\r\n")
		if !strings.HasPrefix(gotRest, c.want) {
			t.Errorf("%s header block:\n got %q\nwant prefix %q", c.path, gotRest, c.want)
		}
		if strings.Contains(got, "traceparent:") {
			t.Errorf("%s must not carry traceparent: %q", c.path, got)
		}
		if !c.withBody && strings.Contains(gotRest, "content-length:") {
			t.Errorf("GET whoami must not send content-length: %q", gotRest)
		}
	}
}

func TestKeepAliveConnReused(t *testing.T) {
	tp := startTap(t, "", `{"ok":true}`)
	cl := &http.Client{
		Transport: &Transport{InsecureSkipVerify: true},
		Timeout:   10 * time.Second,
	}
	post := func() {
		req, _ := http.NewRequest("POST", "https://"+tp.addr+"/alpha/generate", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	post()
	<-tp.requestCh
	post()
	<-tp.requestCh
	if n := atomic.LoadInt32(&tp.accepts); n != 1 {
		t.Errorf("TCP accepts = %d, want 1 (pool should have reused the connection)", n)
	}
}

func TestResponseDecompression(t *testing.T) {
	payload := `{"ok":true,"text":"你好，世界 — streaming-ish payload"}`
	for _, enc := range []string{"gzip", "br"} {
		t.Run(enc, func(t *testing.T) {
			tp := startTap(t, enc, payload)
			cl := &http.Client{
				Transport: &Transport{InsecureSkipVerify: true},
				Timeout:   10 * time.Second,
			}
			req, _ := http.NewRequest("POST", "https://"+tp.addr+"/alpha/generate", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			resp, err := cl.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			got, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read decoded body: %v", err)
			}
			if string(got) != payload {
				t.Errorf("decoded body = %q, want %q", got, payload)
			}
		})
	}
}
