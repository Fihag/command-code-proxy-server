// Package upstream provides an HTTP transport whose on-wire signature —
// TLS ClientHello bytes and HTTP/1.1 header order — is a copy of what the
// official command-code CLI (Node >= 22 global fetch / undici + OpenSSL)
// sends to api.commandcode.ai, rather than Go's crypto/tls + net/http
// defaults. The ClientHello template was captured once from a local Node
// process via tools/tap and is embedded below; every dial parses a fresh
// spec and only rewrites the SNI to the target host.
package upstream

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"

	utls "github.com/refraction-networking/utls"
)

//go:embed node24_clienthello.bin
var baselineClientHello []byte

// Spec returns a fresh utls ClientHelloSpec cloned from the embedded Node
// capture, with the ServerName patched to host. A new spec is parsed per
// dial because utls warns against sharing extension state across
// connections (key shares, padding and randomization are regenerated at
// handshake time from the spec structure).
func Spec(host string) (*utls.ClientHelloSpec, error) {
	fp := &utls.Fingerprinter{AllowBluntMimicry: true}
	spec, err := fp.RawClientHello(baselineClientHello)
	if err != nil {
		return nil, fmt.Errorf("parse baseline client hello: %w", err)
	}
	if !setSNI(spec, host) {
		return nil, fmt.Errorf("baseline client hello has no SNI extension")
	}
	return spec, nil
}

func setSNI(spec *utls.ClientHelloSpec, host string) bool {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i] // strip port for the SNI
	}
	for _, ext := range spec.Extensions {
		if sni, ok := ext.(*utls.SNIExtension); ok {
			sni.ServerName = host
			return true
		}
	}
	// The parser keeps unknown/empty SNI slots as generic extensions; a
	// Node hello always carries SNI, so this path should not be reached.
	return false
}

// HelloLength returns the embedded baseline record size (diagnostics/tests).
func HelloLength() int { return len(bytes.TrimSpace(baselineClientHello)) }
