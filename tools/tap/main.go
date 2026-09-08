// tap is a local probe that captures a TLS client's raw ClientHello bytes
// and the plaintext HTTP/1.1 request it sends afterwards. Used to take one
// reference capture of Node's (the command-code CLI runtime) on-wire
// signature, and to re-verify the proxy replays the same one.
//
// Usage: go run ./tools/tap -listen 127.0.0.1:8443 -out captured.json
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
	"log"
	"math/big"
	"net"
	"os"
	"time"
)

type capture struct {
	Peer        string `json:"peer"`
	ClientHello string `json:"client_hello_hex"`
	HTTPRequest string `json:"http_request"`
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
		NotAfter:              time.Now().Add(24 * 365 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"cctap.local"},
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
	out := flag.String("out", "captured.json", "capture output file (append, one JSON per line)")
	flag.Parse()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("tap listening on %s, capturing to %s", *listen, *out)

	cert := selfSignedCert()
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
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
		go handle(conn, cert, f)
	}
}

// bufConn reads through a bufio.Reader (so already-peeked bytes are not
// lost) while writes bypass the buffer straight to the raw connection.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func handle(conn net.Conn, cert tls.Certificate, out *os.File) {
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

	// Read the plaintext request head (headers only); enough to log order.
	rbr := bufio.NewReader(tc)
	var req []byte
	for {
		line, err := rbr.ReadString('\n')
		req = append(req, line...)
		if err != nil || line == "\r\n" {
			break
		}
	}

	c := capture{
		Peer:        conn.RemoteAddr().String(),
		ClientHello: hex.EncodeToString(helloRec),
		HTTPRequest: string(req),
		CapturedAt:  time.Now().UTC().Format(time.RFC3339),
		NextProto:   tc.ConnectionState().NegotiatedProtocol,
	}
	b, _ := json.Marshal(c)
	fmt.Fprintln(out, string(b))
	log.Printf("captured from %s (alpn=%s, hello=%d bytes)", c.Peer, c.NextProto, len(helloRec))

	body := `{"error":"tap capture"}`
	fmt.Fprintf(tc, "HTTP/1.1 401 Unauthorized\r\ncontent-type: application/json\r\ncontent-length: %d\r\n\r\n%s", len(body), body)
}
