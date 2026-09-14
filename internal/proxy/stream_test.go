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
	return streamToDoneUsage(t, p, upstream, false)
}

func streamToDoneUsage(t *testing.T, p *Proxy, upstream string, includeUsage bool) string {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	p.StreamResponse(w, r, upResp(upstream), "chatcmpl-test", "deepseek/deepseek-v4-flash", 1, includeUsage)
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

// reasoning-delta events (the CLI's wire form for model thinking) must reach
// the client as reasoning_content deltas; start/end carry no payload and must
// not emit chunks.
func TestStreamReasoningBecomesReasoningContent(t *testing.T) {
	p := NewProxy("k")
	got := streamToDone(t, p,
		`{"type":"reasoning-start","id":"reasoning-0"}`+"\n"+
			`{"type":"reasoning-delta","id":"reasoning-0","text":"想"}`+"\n"+
			`{"type":"reasoning-delta","id":"reasoning-0","text":"一下"}`+"\n"+
			`{"type":"reasoning-end","id":"reasoning-0"}`+"\n"+
			`{"type":"text-delta","text":"答案"}`+"\n"+
			`{"type":"finish","finishReason":"stop"}`)
	if !strings.Contains(got, `"reasoning_content":"想"`) || !strings.Contains(got, `"reasoning_content":"一下"`) {
		t.Errorf("reasoning deltas must be forwarded as reasoning_content:\n%s", got)
	}
	if strings.Contains(got, `"reasoning_content":""`) {
		t.Errorf("reasoning-start/end must not emit empty chunks:\n%s", got)
	}
	if strings.Contains(got, `"content":"想"`) {
		t.Errorf("reasoning must not leak into content:\n%s", got)
	}
	if !strings.Contains(got, `"content":"答案"`) {
		t.Errorf("text delta must still be content:\n%s", got)
	}
}

// stream_options.include_usage must produce the terminal usage chunk (empty
// choices, token counts) before [DONE]; without the flag there must be none.
func TestStreamUsageChunkHonorsIncludeUsage(t *testing.T) {
	upstream := `{"type":"text-delta","text":"hi"}` + "\n" +
		`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":15410,"outputTokens":12}}`
	p := NewProxy("k")

	with := streamToDoneUsage(t, p, upstream, true)
	if !strings.Contains(with, `"prompt_tokens":15410`) || !strings.Contains(with, `"total_tokens":15422`) {
		t.Errorf("include_usage must emit terminal usage chunk:\n%s", with)
	}
	usageChunks := strings.Count(with, `"choices":[]`)
	if usageChunks != 1 {
		t.Errorf("want exactly 1 empty-choices usage chunk, got %d:\n%s", usageChunks, with)
	}

	without := streamToDoneUsage(t, p, upstream, false)
	if strings.Contains(without, `"choices":[]`) {
		t.Errorf("usage chunk must be omitted without include_usage:\n%s", without)
	}
}

// Non-stream: reasoning accumulates into message.reasoning_content.
func TestNonStreamReasoningCollected(t *testing.T) {
	p := NewProxy("k")
	w := httptest.NewRecorder()
	p.NonStreamResponse(w, upResp(
		`{"type":"reasoning-delta","text":"思考"}`+"\n"+
			`{"type":"text-delta","text":"答案"}`+"\n"+
			`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":5,"outputTokens":2}}`),
		"chatcmpl-test", "m", 1)
	got := w.Body.String()
	if !strings.Contains(got, `"reasoning_content":"思考"`) {
		t.Errorf("non-stream reasoning_content missing: %s", got)
	}
	if !strings.Contains(got, `"content":"答案"`) {
		t.Errorf("content wrong: %s", got)
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

// A single NDJSON event larger than any Scanner token cap must still be
// delivered in full: upstream legitimately puts a whole large tool-call input
// on one line, and bufio.Scanner would abort the stream with ErrTooLong.
func TestStreamVeryLongSingleLine(t *testing.T) {
	p := NewProxy("k")
	huge := strings.Repeat("x", 2*1024*1024)
	got := streamToDone(t, p, `{"type":"text-delta","text":"`+huge+`"}`)
	if !strings.Contains(got, huge) {
		t.Fatalf("2MB delta truncated or dropped (stream body %d bytes)", len(got))
	}
	w := httptest.NewRecorder()
	p.NonStreamResponse(w, upResp(
		`{"type":"text-delta","text":"`+huge+`"}`+"\n"+
			`{"type":"finish","finishReason":"stop"}`),
		"chatcmpl-test", "m", 1)
	if !strings.Contains(w.Body.String(), huge) {
		t.Fatalf("2MB content missing from non-stream body (%d bytes)", w.Body.Len())
	}
}
