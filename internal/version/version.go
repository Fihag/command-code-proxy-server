// Package version reports the CommandCode CLI version this proxy impersonates.
//
// The real CLI force-updates itself at process start, then keeps that exact
// version for the whole process (lifecycle beacon, x-command-code-version
// header and behaviour all agree on it). We mirror that: Resolve() looks up
// the npm "latest" once, bounded by a timeout, and freezes the answer for the
// process lifetime. Changing it mid-process would contradict the cliVersion
// already sent in this process's lifecycle-events beacon, which is the one
// cross-check a server can reliably make.
//
// If the lookup fails (offline, blocked, malformed answer) we fall back to
// Baseline — the version whose wire behaviour (TLS hello, header templates,
// built-in tools) is replayed via tools/recapture.mjs. Bumping Baseline after
// a re-capture is still recommended so the offline fallback stays current.
package version

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// Baseline is the command-code version whose wire behaviour is replayed, and
// the value kept when the npm lookup fails.
const Baseline = "1.50.1"

// npmLatestURL is the registry endpoint for the published CLI package.
// A var so tests can point resolution at a stub server.
var npmLatestURL = "https://registry.npmjs.org/command-code/latest"

// resolveTimeout bounds the startup lookup. Unlike the old implementation this
// is NOT on any request path (main calls Resolve once before serving), so a
// stalled registry costs startup latency at most, never a chat request.
const resolveTimeout = 5 * time.Second

var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+[-.\dA-Za-z]*$`)

var (
	versionMu sync.RWMutex
	current   = Baseline
	resolved  bool
)

// GetCommandCodeVersion returns the impersonated CLI version frozen for this
// process. Safe for concurrent use on every request.
func GetCommandCodeVersion() string {
	versionMu.RLock()
	defer versionMu.RUnlock()
	return current
}

// Resolve queries npm for the latest published CLI version once and freezes
// the result. Call from main before any request or beacon goes out; repeat
// calls are no-ops. On any failure the Baseline stays in effect.
func Resolve() {
	versionMu.Lock()
	if resolved {
		versionMu.Unlock()
		return
	}
	versionMu.Unlock()

	v := fetchLatestFrom(npmLatestURL)

	versionMu.Lock()
	resolved = true
	if v != "" {
		current = v
	}
	versionMu.Unlock()
}

func fetchLatestFrom(url string) string {
	client := &http.Client{Timeout: resolveTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return ""
	}
	// The header goes verbatim onto the wire template: never accept anything
	// that does not look like a semver release string.
	if !versionPattern.MatchString(doc.Version) {
		return ""
	}
	return doc.Version
}
