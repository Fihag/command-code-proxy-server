package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// End-to-end handler test against an in-process upstream stub. It pins two
// load-bearing behaviours:
//
//  1. A present-but-empty bearer token ("Bearer ") falls back to the
//     server-side default key instead of forwarding an empty key upstream.
//     With the recommended COMMANDCODE_API_KEY setup (no -api-key, no client
//     key) every request used to 401.
//  2. The startup beacon (whoami → lifecycle-events) is strictly ordered
//     before the first generate: the handler gates on it so upstream never
//     sees generate traffic that lacks its own birth events.
func TestHandlerBeaconOrderingAndEmptyBearerFallback(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var auths []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/alpha/whoami", "/alpha/lifecycle-events":
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{}`)
		case "/alpha/generate":
			w.Header().Set("Content-Type", "application/x-ndjson")
			io.WriteString(w,
				`{"type":"text-delta","text":"fine"}`+"\n"+
					`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":3,"outputTokens":1}}`+"\n")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer stub.Close()

	p := NewProxy("server-default-key")
	p.BaseURL = stub.URL
	p.Client = stub.Client()

	body := strings.NewReader(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	r.Header.Set("Authorization", "Bearer ") // present but EMPTY token
	w := httptest.NewRecorder()
	p.HandleChatCompletions(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"content":"fine"`) {
		t.Fatalf("unexpected completion body: %s", w.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	firstBirth, firstGen := -1, -1
	for i, path := range paths {
		switch path {
		case "/alpha/generate":
			if firstGen == -1 {
				firstGen = i
			}
		case "/alpha/whoami", "/alpha/lifecycle-events":
			if firstBirth == -1 {
				firstBirth = i
			}
		}
	}
	if firstGen == -1 {
		t.Fatalf("generate never reached upstream: %v", paths)
	}
	if firstBirth == -1 {
		t.Fatalf("beacon never reached upstream: %v", paths)
	}
	if firstBirth > firstGen {
		t.Errorf("birth events (idx %d) must precede generate (idx %d): %v", firstBirth, firstGen, paths)
	}
	for i, a := range auths {
		if a != "Bearer server-default-key" {
			t.Errorf("%s: Authorization = %q, want the empty-bearer fallback to server key", paths[i], a)
		}
	}
}
