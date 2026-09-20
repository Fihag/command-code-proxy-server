package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// generateSrv answers /alpha/generate with 5xx for the first failTimes calls,
// then a valid non-stream NDJSON completion; it counts only generate hits so
// unrelated beacon traffic cannot skew assertions.
func generateSrv(failTimes int32, code int, hits *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/alpha/generate") {
			http.NotFound(w, r)
			return
		}
		n := atomic.AddInt32(hits, 1)
		if n <= failTimes {
			w.WriteHeader(code)
			w.Write([]byte(`{"success":false,"error":{"code":"INTERNAL_SERVER_ERROR","status":500,"message":""}}`))
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte("{\"type\":\"text-delta\",\"text\":\"ok\"}\n{\"type\":\"finish\",\"finishReason\":\"stop\"}\n"))
	}))
}

func chatReq() *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	r.Header.Set("Authorization", "Bearer testkey")
	return r
}

func newTestProxy(url string) *Proxy {
	p := NewProxy("k")
	p.BaseURL = url
	p.Client = http.DefaultClient
	close(p.beaconGate) // skip the birth-event gate; it is covered by beacon tests
	return p
}

// Upstream 5xx is transient by nature (empty-message INTERNAL_SERVER_ERROR
// from a backend hiccup): the handler must re-issue the request and only
// surface the error after the attempts run out.
func TestUpstream5xxIsRetried(t *testing.T) {
	var hits int32
	srv := generateSrv(2, http.StatusInternalServerError, &hits)
	defer srv.Close()

	w := httptest.NewRecorder()
	newTestProxy(srv.URL).HandleChatCompletions(w, chatReq())

	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("generate calls = %d, want 3 (two 500s + success)", got)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"content":"ok"`) {
		t.Fatalf("success body missing content: %s", w.Body.String())
	}
}

// Exhausted retries must still surface the failure, never a fake success.
func TestUpstream5xxAfterRetriesFails(t *testing.T) {
	var hits int32
	srv := generateSrv(10, http.StatusInternalServerError, &hits)
	defer srv.Close()

	w := httptest.NewRecorder()
	newTestProxy(srv.URL).HandleChatCompletions(w, chatReq())

	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("generate calls = %d, want 3 (max attempts)", got)
	}
	if w.Code == http.StatusOK {
		t.Fatalf("must not report success after exhausted retries: %s", w.Body.String())
	}
}

// 4xx is the client's/payload's fault, not a hiccup: it must NOT be retried.
func TestUpstream4xxIsNotRetried(t *testing.T) {
	var hits int32
	srv := generateSrv(10, http.StatusBadRequest, &hits)
	defer srv.Close()

	w := httptest.NewRecorder()
	newTestProxy(srv.URL).HandleChatCompletions(w, chatReq())

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("generate calls = %d, want 1 (4xx must not retry)", got)
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 passthrough", w.Code)
	}
}
