package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const rateLimitMsg = `Rate limit exceeded for deepseek/deepseek-v4.1-flash: this team's limit of 10000 requests per minute (across all regions) was reached. Retry after 60s.`
const quotaMsg = `You exceeded your current quota, please check your plan and billing details`

func TestUpstreamThrottleType(t *testing.T) {
	cases := []struct {
		msg       string
		wantType  string
		wantAfter string
	}{
		{rateLimitMsg, "rate_limit_error", "60"},
		{quotaMsg, "insufficient_quota", ""},
		{"upstream blew up", "api_error", ""},
		{"Rate limit exceeded without hint", "rate_limit_error", "60"},
	}
	for _, tc := range cases {
		gotType, gotAfter := upstreamThrottleType(tc.msg)
		if gotType != tc.wantType || gotAfter != tc.wantAfter {
			t.Errorf("upstreamThrottleType(%q) = (%q,%q), want (%q,%q)", tc.msg, gotType, gotAfter, tc.wantType, tc.wantAfter)
		}
	}
}

// ndjsonSrv answers generate with raw NDJSON body (200) — the shape upstream
// uses to deliver in-stream error events.
func ndjsonSrv(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/alpha/generate") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(body))
	}))
}

func TestNonStreamRateLimitGets429(t *testing.T) {
	srv := ndjsonSrv(`{"type":"error","error":{"message":"` + rateLimitMsg + `","statusCode":429}}` + "\n")
	defer srv.Close()
	w := httptest.NewRecorder()
	newTestProxy(srv.URL).HandleChatCompletions(w, chatReq())

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body %s)", w.Code, w.Body.String())
	}
	if ra := w.Header().Get("Retry-After"); ra != "60" {
		t.Errorf("Retry-After = %q, want 60", ra)
	}
	if !strings.Contains(w.Body.String(), `"rate_limit_error"`) {
		t.Errorf("body missing rate_limit_error type: %s", w.Body.String())
	}
}

func TestNonStreamQuotaGetsInsufficientQuota(t *testing.T) {
	srv := ndjsonSrv(`{"type":"error","error":{"message":"` + quotaMsg + `","statusCode":429}}` + "\n")
	defer srv.Close()
	w := httptest.NewRecorder()
	newTestProxy(srv.URL).HandleChatCompletions(w, chatReq())

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"insufficient_quota"`) {
		t.Errorf("body missing insufficient_quota type: %s", w.Body.String())
	}
}

func TestStreamRateLimitFrameType(t *testing.T) {
	srv := ndjsonSrv(
		`{"type":"text-delta","text":"par"}` + "\n" +
			`{"type":"error","error":{"message":"` + rateLimitMsg + `","statusCode":429}}` + "\n")
	defer srv.Close()

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	r.Header.Set("Authorization", "Bearer testkey")
	w := httptest.NewRecorder()
	newTestProxy(srv.URL).HandleChatCompletions(w, r)

	got := w.Body.String()
	if !strings.Contains(got, `"rate_limit_error"`) {
		t.Fatalf("error frame must carry rate_limit_error type: %s", got)
	}
	if !strings.Contains(got, `"par"`) {
		t.Fatalf("deltas before the error must still be forwarded: %s", got)
	}
	if !strings.HasSuffix(got, "data: [DONE]\n\n") {
		t.Fatalf("stream must still close cleanly: %s", got[len(got)-60:])
	}
}
