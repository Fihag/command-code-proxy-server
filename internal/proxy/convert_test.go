package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dev2k6/command-code-proxy-server/internal/api"
)

// Upstream rejects null content unless role is tool ("expected \"tool\" at
// ...role"). Clients replay reasoning-only or empty assistant turns with
// content: null, so the converter must never emit null content.
func TestConvertMessagesNeverEmitsNullContent(t *testing.T) {
	cases := []struct {
		name string
		msg  api.OpenAIMessage
	}{
		{"assistant null content", api.OpenAIMessage{Role: "assistant", Content: nil}},
		{"assistant empty string", api.OpenAIMessage{Role: "assistant", Content: ""}},
		{"user empty content array", api.OpenAIMessage{Role: "user", Content: []any{}}},
		{"user unknown content type", api.OpenAIMessage{Role: "user", Content: map[string]any{"unexpected": true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := ConvertMessages([]api.OpenAIMessage{tc.msg})
			if len(out) != 1 {
				t.Fatalf("got %d messages, want 1", len(out))
			}
			raw, err := json.Marshal(out[0])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), `"content":null`) {
				t.Fatalf("content serialized as null: %s", raw)
			}
			if len(out[0].Content) == 0 {
				t.Fatalf("empty content parts: %s", raw)
			}
			part := out[0].Content[0]
			if part.Type != "text" || part.Text == nil {
				t.Fatalf("fallback part is not a text part: %+v", part)
			}
		})
	}
}

func TestConvertMessagesToolCallOnlyAssistantKeepsToolCallParts(t *testing.T) {
	out := ConvertMessages([]api.OpenAIMessage{
		{
			Role:    "assistant",
			Content: nil,
			ToolCalls: []api.ToolCall{{
				ID:       "call_1",
				Function: api.FunctionCall{Name: "Read", Arguments: `{"file_path":"a"}`},
			}},
		},
	})
	if len(out) != 1 || len(out[0].Content) != 1 {
		t.Fatalf("got %+v", out)
	}
	part := out[0].Content[0]
	if part.Type != "tool-call" || part.ToolName == nil || *part.ToolName != "Read" {
		t.Fatalf("expected tool-call part, got %+v", part)
	}
}
