package proxy

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if !isUUID(ccBody.ThreadID) {
		t.Fatalf("threadId = %q, want uuid v4", ccBody.ThreadID)
	}

	httpReq, err := p.CreateUpstreamRequest(context.Background(), ccBody, "k")
	if err != nil {
		t.Fatalf("CreateUpstreamRequest: %v", err)
	}
	for h, want := range map[string]string{
		"User-Agent":        "cli",
		"x-session-id":      ccBody.ThreadID,
		"x-project-slug":    "demo-proj",
		"x-taste-learning":  "true",
		"x-cli-environment": "production",
	} {
		if got := httpReq.Header.Get(h); got != want {
			t.Errorf("header %s = %q, want %q", h, got, want)
		}
	}
}

// isUUID checks the 8-4-4-4-12 lowercase-hex shape the CLI uses for thread
// ids / x-session-id.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				return false
			}
		}
	}
	return true
}

// TestWireBodyShape asserts the serialized body matches the real CLI's
// /alpha/generate payload (authoritative tap capture of 1.50.1): null
// memory/taste/skills, permissionMode + uuid threadId present, no mode key,
// system as a block array, tools non-empty, temperature absent when unset.
func TestWireBodyShape(t *testing.T) {
	p := NewProxy("k")
	ccBody, err := p.BuildRequest(api.OpenAIChatRequest{
		Model: "deepseek-v4-pro",
		Messages: []api.OpenAIMessage{
			{Role: "system", Content: "be nice"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ccBody)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{
		`"memory":null`, `"taste":null`, `"skills":null`,
		`"permissionMode":"standard"`,
		`"stream":true`, `"system":[{"type":"text","text":"be nice"}]`, `"name":"read_file"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("body missing %s: %s", want, s)
		}
	}
	for _, absent := range []string{`"mode"`, `"temperature"`, `"promptCache"`} {
		if strings.Contains(s, absent) {
			t.Errorf("body must not contain %s: %s", absent, s)
		}
	}
	// Field order must follow the CLI: config first, params last.
	if idx := strings.Index(s, `"config"`); idx != 1 {
		t.Errorf(`"config" not first: idx=%d body=%s`, idx, s)
	}
	if idx := strings.Index(s, `"params"`); !strings.Contains(s[:idx], `"threadId":"`) {
		t.Errorf(`"params" not after threadId: %s`, s)
	}
	if idx := strings.Index(s, `"threadId":"`); !strings.HasPrefix(s[idx:], `"threadId":"`+ccBody.ThreadID+`","params"`) {
		t.Errorf(`threadId must equal x-session-id uuid and precede params: %s`, s[idx:idx+80])
	}
}

// TestEnvConfigCollectsRealValues checks config fields are gathered from the
// proxy's own environment instead of placeholders.
func TestEnvConfigCollectsRealValues(t *testing.T) {
	p := NewProxy("k")
	p.envCfg.gather = func() api.CCConfig {
		return api.CCConfig{
			WorkingDir:    "/tmp/demo",
			Environment:   "linux",
			Structure:     []string{"README.md", "src"},
			IsGitRepo:     true,
			CurrentBranch: "main",
			MainBranch:    "main",
			GitStatus:     "Working tree clean",
			RecentCommits: []string{"abc1234 msg"},
		}
	}
	ccBody, err := p.BuildRequest(api.OpenAIChatRequest{
		Model:    "m",
		Messages: []api.OpenAIMessage{{Role: "user", Content: "x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ccBody.Config.WorkingDir != "/tmp/demo" || !ccBody.Config.IsGitRepo {
		t.Errorf("config not wired from env snapshot: %+v", ccBody.Config)
	}
	if ccBody.Config.Date != time.Now().UTC().Format("2006-01-02") {
		t.Errorf("date = %q", ccBody.Config.Date)
	}

	// And against the real environment: workingDir absolute, structure filled.
	p2 := NewProxy("k")
	cfg := p2.envCfg.get()
	if !filepath.IsAbs(cfg.WorkingDir) && cfg.WorkingDir != "." {
		t.Errorf("workingDir not absolute: %q", cfg.WorkingDir)
	}
	if len(cfg.Structure) == 0 {
		t.Error("structure empty; expected top-level entries of repo dir")
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
		t.Errorf("same user should reuse thread: %q vs %q", first.ThreadID, again.ThreadID)
	}
	if !isUUID(first.ThreadID) {
		t.Errorf("threadId not uuid: %q", first.ThreadID)
	}
	if first.ThreadID == other.ThreadID {
		t.Error("different users must not share a thread")
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
		t.Errorf("multi-turn replay must keep thread: %q vs %q", a.ThreadID, b.ThreadID)
	}
}
