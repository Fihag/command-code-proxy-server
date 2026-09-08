package proxy

import (
	"context"
	"strings"
	"testing"

	"github.com/dev2k6/command-code-proxy-server/internal/api"
)

func TestBuildRequestCarriesCLIHeaders(t *testing.T) {
	p := NewProxy("k")
	p.SetProjectSlug("demo-proj")

	req := api.OpenAIChatRequest{
		Model: "deepseek-v4-pro",
		Messages: []api.OpenAIMessage{
			{Role: "user", Content: "hello"},
		},
	}
	ccBody, err := p.BuildRequest(req)
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if !strings.HasPrefix(ccBody.ThreadID, "sess_") || len(ccBody.ThreadID) != 5+16 {
		t.Fatalf("threadId = %q, want sess_ + 16 hex chars", ccBody.ThreadID)
	}

	httpReq, err := p.CreateUpstreamRequest(context.Background(), ccBody, "k")
	if err != nil {
		t.Fatalf("CreateUpstreamRequest: %v", err)
	}
	for h, want := range map[string]string{
		"User-Agent":        "cli",
		"x-session-id":      ccBody.ThreadID,
		"x-project-slug":    "demo-proj",
		"x-taste-learning":  "false",
		"x-cli-environment": "production",
	} {
		if got := httpReq.Header.Get(h); got != want {
			t.Errorf("header %s = %q, want %q", h, got, want)
		}
	}
}

func TestSessionStickyPerConversation(t *testing.T) {
	p := NewProxy("k")
	msg := func(s string) []api.OpenAIMessage {
		return []api.OpenAIMessage{{Role: "user", Content: s}}
	}
	first, err := p.BuildRequest(api.OpenAIChatRequest{Model: "m", User: "alice", Messages: msg("hi")})
	if err != nil {
		t.Fatal(err)
	}
	again, err := p.BuildRequest(api.OpenAIChatRequest{Model: "m", User: "alice", Messages: msg("hi again")})
	if err != nil {
		t.Fatal(err)
	}
	other, err := p.BuildRequest(api.OpenAIChatRequest{Model: "m", User: "bob", Messages: msg("hi")})
	if err != nil {
		t.Fatal(err)
	}
	if first.ThreadID != again.ThreadID {
		t.Errorf("same user should reuse session: %q vs %q", first.ThreadID, again.ThreadID)
	}
	if first.ThreadID == other.ThreadID {
		t.Error("different users must not share a session")
	}
}

func TestSessionStableWithoutUserField(t *testing.T) {
	p := NewProxy("k")
	mk := func() api.OpenAIChatRequest {
		return api.OpenAIChatRequest{
			Model: "m",
			Messages: []api.OpenAIMessage{
				{Role: "system", Content: "s"},
				{Role: "user", Content: "first question"},
				{Role: "assistant", Content: "answer"},
				{Role: "user", Content: "follow-up"},
			},
		}
	}
	a, err := p.BuildRequest(mk())
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.BuildRequest(mk())
	if err != nil {
		t.Fatal(err)
	}
	if a.ThreadID != b.ThreadID {
		t.Errorf("multi-turn replay must keep session: %q vs %q", a.ThreadID, b.ThreadID)
	}
}
