// tap is a local probe that captures a TLS client's raw ClientHello bytes
// and the plaintext HTTP/1.1 exchanges it sends afterwards. Used to take
// reference captures of Node's (the command-code CLI runtime) on-wire
// signature, and to re-verify the proxy replays the same one.
//
// Canned mode (no -upstream): replies a fixed 401 to each request.
// Forwarding mode (-upstream IP:443): transparent MITM — requests are
// forwarded to the real server at that IP (the IP is what bypasses the
// hosts-file redirect), responses are streamed back verbatim. Authorization
// headers are redacted before storing.
//
// Usage: go run ./tools/tap -listen 127.0.0.1:443 -upstream 203.0.113.7:443 -out captured.jsonl
package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type capture struct {
	Peer        string `json:"peer"`
	ClientHello string `json:"client_hello_hex"`
	HTTPRequest string `json:"http_request"`
	HTTPBody    string `json:"http_body"`
	CapturedAt  string `json:"captured_at"`
	NextProto   string `json:"negotiated_alpn"`
}

func selfSignedCert() tls.Certificate {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "cctap.local"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"cctap.local", "api.commandcode.ai"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// peekClientHello reads from the raw connection until a complete TLS
// handshake record (type 0x16) containing the ClientHello is buffered,
// and returns exactly that first record's bytes. The data stays in the
// bufio buffer, so the TLS server can still consume it.
func peekClientHello(br *bufio.Reader) ([]byte, error) {
	for {
		buf := br.Buffered()
		if buf >= 5 {
			head, err := br.Peek(buf)
			if err != nil {
				return nil, err
			}
			if head[0] == 0x16 {
				recLen := int(binary.BigEndian.Uint16(head[3:5]))
				if buf >= 5+recLen {
					hello, err := br.Peek(5 + recLen)
					if err != nil {
						return nil, err
					}
					out := make([]byte, len(hello))
					copy(out, hello)
					return out, nil
				}
			}
		}
		// Force more data into the buffer without consuming.
		if _, err := br.Peek(1); err != nil {
			return nil, err
		}
	}
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8443", "tap listen address")
	upstream := flag.String("upstream", "", "forward to this real IP:port (empty = canned 401 mode)")
	out := flag.String("out", "captured.jsonl", "capture output file (append, one JSON per line)")
	flag.Parse()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	mode := "canned"
	if *upstream != "" {
		mode = "forward -> " + *upstream
	}
	log.Printf("tap listening on %s (%s), capturing to %s", *listen, mode, *out)

	cert := selfSignedCert()
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Println(err)
			continue
		}
		go handle(conn, cert, f, *upstream)
	}
}

// bufConn reads through a bufio.Reader (so already-peeked bytes are not
// lost) while writes bypass the buffer straight to the raw connection.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func handle(conn net.Conn, cert tls.Certificate, out *os.File, upstreamAddr string) {
	defer conn.Close()
	br := bufio.NewReader(conn)

	helloRec, err := peekClientHello(br)
	if err != nil {
		log.Printf("peek client hello: %v", err)
		return
	}

	tc := tls.Server(&bufConn{Conn: conn, r: br}, &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"http/1.1"},
	})
	if err := tc.Handshake(); err != nil {
		log.Printf("handshake: %v", err)
		return
	}
	log.Printf("hello captured from %s (%d bytes, alpn=%q)", conn.RemoteAddr().String(), len(helloRec), tc.ConnectionState().NegotiatedProtocol)

	rbr := bufio.NewReaderSize(tc, 64*1024)
	for {
		var head strings.Builder
		contentLen := -1
		targetHost := ""
		for {
			line, err := rbr.ReadString('\n')
			if err != nil {
				return
			}
			head.WriteString(line)
			if line == "\r\n" {
				break
			}
			ln := strings.ToLower(line)
			if n, ok := strings.CutPrefix(ln, "content-length:"); ok {
				contentLen, _ = strconv.Atoi(strings.TrimSpace(n))
			}
			if h, ok := strings.CutPrefix(ln, "host:"); ok {
				targetHost = strings.TrimSpace(h)
			}
			if strings.HasPrefix(ln, "transfer-encoding:") {
				log.Printf("chunked request not supported, refusing")
				fmt.Fprint(tc, "HTTP/1.1 501 Not Implemented\r\ncontent-length: 0\r\n\r\n")
				return
			}
		}
		var body []byte
		if contentLen > 0 {
			body = make([]byte, contentLen)
			if _, err := io.ReadFull(rbr, body); err != nil {
				log.Printf("read body: %v", err)
				return
			}
		}

		c := capture{
			Peer:        conn.RemoteAddr().String(),
			ClientHello: hex.EncodeToString(helloRec),
			HTTPRequest: redactAuth(head.String()),
			HTTPBody:    string(body),
			CapturedAt:  time.Now().UTC().Format(time.RFC3339),
			NextProto:   tc.ConnectionState().NegotiatedProtocol,
		}
		b, _ := json.Marshal(c)
		fmt.Fprintln(out, string(b))
		out.Sync()
		log.Printf("captured %d-byte request to %s", len(body), strings.SplitN(c.HTTPRequest, "\r\n", 2)[0])

		if upstreamAddr == "" {
			respb := `{"error":"tap capture"}`
			fmt.Fprintf(tc, "HTTP/1.1 401 Unauthorized\r\ncontent-type: application/json\r\ncontent-length: %d\r\n\r\n%s", len(respb), respb)
			continue
		}

		// Forward to the real upstream with the original hostname as SNI.
		sni := targetHost
		if i := strings.IndexByte(sni, ':'); i >= 0 {
			sni = sni[:i]
		}
		up, err := tls.Dial("tcp", upstreamAddr, &tls.Config{ServerName: sni, NextProtos: []string{"http/1.1"}})
		if err != nil {
			log.Printf("upstream dial: %v", err)
			fmt.Fprint(tc, "HTTP/1.1 502 Bad Gateway\r\ncontent-length: 0\r\n\r\n")
			continue
		}
		// Rewrite the request line for the upstream (it is already absolute-path
		// style) and re-send the head verbatim plus body.
		if _, err := up.Write([]byte(head.String())); err != nil {
			up.Close()
			continue
		}
		if len(body) > 0 {
			if _, err := up.Write(body); err != nil {
				up.Close()
				continue
			}
		}
		// Stream the response back until the upstream closes.
		_, _ = io.Copy(tc, up)
		up.Close()
	}
}

// redactAuth replaces any Authorization header value so captures never
// carry the account key.
func redactAuth(head string) string {
	lines := strings.Split(head, "\r\n")
	for i, l := range lines {
		if _, ok := strings.CutPrefix(strings.ToLower(l), "authorization:"); ok {
			if idx := strings.IndexByte(l, ':'); idx >= 0 {
				lines[i] = l[:idx] + ": [redacted]"
			}
		}
	}
	return strings.Join(lines, "\r\n")
}
