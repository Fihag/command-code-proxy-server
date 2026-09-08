package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// upResp builds a fake upstream 200 response whose body is the given raw
// SSE-ish lines, exactly the shape CommandCode sends to the CLI.
func upResp(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

func streamToDone(t *testing.T, p *Proxy, upstream string) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	p.StreamResponse(w, r, upResp(upstream), "chatcmpl-test", "deepseek/deepseek-v4-flash", 1)
	return w.Body.String()
}

// Upstream ending without a finish event must still close the SSE protocol:
// a stream missing [DONE] leaves well-behaved OpenAI clients hanging on read.
func TestStreamClosedWithoutFinishGetsDONE(t *testing.T) {
	p := NewProxy("k")
	got := streamToDone(t, p, `{"type":"text-delta","text":"partial answer"}`)
	if !strings.Contains(got, `"finish_reason":"stop"`) {
		t.Errorf("missing synthesized finish chunk:\n%s", got)
	}
	if !strings.HasSuffix(got, "data: [DONE]\n\n") {
		t.Errorf("stream must end with [DONE], got tail %q", got[len(got)-40:])
	}
}

func TestStreamErrorGetsFrameAndDONE(t *testing.T) {
	p := NewProxy("k")
	got := streamToDone(t, p,
		`{"type":"text-delta","text":"par"}`+"\n"+
			`{"type":"error","error":{"message":"upstream blew up","statusCode":500}}`)
	if !strings.Contains(got, `"error"`) || !strings.Contains(got, "upstream blew up") {
		t.Errorf("error event must surface an error frame:\n%s", got)
	}
	if !strings.HasSuffix(got, "data: [DONE]\n\n") {
		t.Errorf("error stream must still end with [DONE]:\n%s", got)
	}
}

// tool-use followed by an aggregated tool-call for the SAME id must be
// forwarded once, not twice on two indexes.
func TestStreamToolUseThenAggregatedCallNoDuplicate(t *testing.T) {
	p := NewProxy("k")
	got := streamToDone(t, p,
		`{"type":"tool-use","toolCallId":"call_7","toolName":"read_file"}`+"\n"+
			`{"type":"tool-delta","text":"{\"path\":\"a.txt\"}"}`+"\n"+
			`{"type":"tool-call","toolCallId":"call_7","toolName":"read_file","input":{"path":"a.txt"}}`)
	if n := strings.Count(got, `"id":"call_7"`); n != 1 {
		t.Errorf("tool call id emitted %d times, want 1:\n%s", n, got)
	}
	if strings.Contains(got, `"index":1`) {
		t.Errorf("aggregated tool-call allocated a second index:\n%s", got)
	}
	if !strings.Contains(got, `"finish_reason":"tool_calls"`) {
		t.Errorf("stream with tool calls must finish with tool_calls:\n%s", got)
	}
}

// Non-stream: an upstream error event must become an HTTP error, never a 200
// with empty content and zero usage that clients record as a real completion.
func TestNonStreamErrorNotMasqueradingAsSuccess(t *testing.T) {
	p := NewProxy("k")
	w := httptest.NewRecorder()
	p.NonStreamResponse(w, upResp(`{"type":"error","error":{"message":"quota blew up"}}`), "chatcmpl-test", "m", 1)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s, want 502", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "quota blew up") {
		t.Errorf("error message must propagate: %s", w.Body.String())
	}
}

func TestNonStreamNormalStillSucceeds(t *testing.T) {
	p := NewProxy("k")
	w := httptest.NewRecorder()
	p.NonStreamResponse(w, upResp(
		`{"type":"text-delta","text":"hello"}`+"\n"+
			`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":5,"outputTokens":2}}`),
		"chatcmpl-test", "m", 1)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	got := w.Body.String()
	if !strings.Contains(got, `"content":"hello"`) || !strings.Contains(got, `"total_tokens":7`) {
		t.Errorf("normal non-stream body wrong: %s", got)
	}
}
