package version

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// resetVersionState restores the package globals around each test; Resolve
// is once-per-process, so tests must reset `resolved`/`current` deliberately.
func resetVersionState(t *testing.T) {
	t.Helper()
	versionMu.Lock()
	prevCur, prevRes := current, resolved
	versionMu.Unlock()
	t.Cleanup(func() {
		versionMu.Lock()
		current, resolved = prevCur, prevRes
		versionMu.Unlock()
	})
}

func stubRegistry(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestResolveFreezesLatest(t *testing.T) {
	resetVersionState(t)
	srv := stubRegistry(t, `{"version":"9.9.9"}`, http.StatusOK)
	oldURL := npmLatestURL
	npmLatestURL = srv.URL
	t.Cleanup(func() { npmLatestURL = oldURL })

	Resolve()
	if got := GetCommandCodeVersion(); got != "9.9.9" {
		t.Fatalf("after Resolve = %q, want 9.9.9", got)
	}

	// A later registry change must NOT move the frozen process version.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"version":"8.8.8"}`))
	})
	Resolve() // second call is a no-op
	if got := GetCommandCodeVersion(); got != "9.9.9" {
		t.Errorf("version changed after freeze: %q", got)
	}
}

func TestResolveKeepsBaselineOnFailure(t *testing.T) {
	resetVersionState(t)
	srv := stubRegistry(t, `not json`, http.StatusOK)
	oldURL := npmLatestURL
	npmLatestURL = srv.URL
	t.Cleanup(func() { npmLatestURL = oldURL })

	Resolve()
	if got := GetCommandCodeVersion(); got != Baseline {
		t.Errorf("on malformed answer version = %q, want Baseline %q", got, Baseline)
	}
}

func TestResolveRejectsNonSemver(t *testing.T) {
	resetVersionState(t)
	// Anything that does not look like a release version must never reach the
	// wire header (injection / registry tamper guard).
	srv := stubRegistry(t, `{"version":"1.0\r\nx-evil: 1"}`, http.StatusOK)
	oldURL := npmLatestURL
	npmLatestURL = srv.URL
	t.Cleanup(func() { npmLatestURL = oldURL })

	Resolve()
	if got := GetCommandCodeVersion(); got != Baseline {
		t.Errorf("non-semver accepted: %q", got)
	}
}

func TestFetchLatestFromStatusAndShape(t *testing.T) {
	oldURL := npmLatestURL
	t.Cleanup(func() { npmLatestURL = oldURL })

	cases := []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{"ok", `{"version":"1.52.3"}`, http.StatusOK, "1.52.3"},
		{"prerelease ok", `{"version":"2.0.0-beta.1"}`, http.StatusOK, "2.0.0-beta.1"},
		{"non-200", `{"version":"1.52.3"}`, http.StatusInternalServerError, ""},
		{"missing field", `{}`, http.StatusOK, ""},
		{"garbage string", `{"version":"latest"}`, http.StatusOK, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := stubRegistry(t, c.body, c.status)
			if got := fetchLatestFrom(srv.URL); got != c.want {
				t.Errorf("fetchLatestFrom = %q, want %q", got, c.want)
			}
		})
	}
}
