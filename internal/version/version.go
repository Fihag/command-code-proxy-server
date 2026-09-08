// Package version reports the CommandCode CLI version this proxy impersonates.
//
// The version is pinned to the wire-behaviour baseline captured by tools/tap
// and must move only together with that baseline. x-command-code-version is
// sent on every generate/lifecycle/fingerprint call, while the TLS
// ClientHello, header templates and built-in tool set all replay
// command-code 1.50.1; a live npm "latest" sitting on top of frozen 1.50.1
// behaviour is a contradiction the server can cross-check. The npm lookup
// also used to hang the very first chat request: its result was discarded by
// init() and the request path then did a synchronous http.Get with no
// timeout, so a stalled registry blocked the proxy indefinitely.
//
// After a re-capture (README: 重捕获指纹基准) bump Baseline to the version
// tools/recapture.mjs prints.
package version

// Baseline is the command-code version whose wire behaviour is replayed.
const Baseline = "1.50.1"

// GetCommandCodeVersion returns the impersonated CLI version. Kept as a
// function so call sites stay uniform.
func GetCommandCodeVersion() string { return Baseline }
