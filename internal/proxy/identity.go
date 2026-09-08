package proxy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/dev2k6/command-code-proxy-server/internal/api"
	"github.com/google/uuid"
)

// The upstream API correlates /alpha/generate traffic with three identity
// fields (authoritative capture via tools/tap against command-code 1.50.1):
// the x-session-id header and the body threadId carry the SAME per-conversation
// uuid v4, while lifecycle-events carry a separate process-level "sess_..." id
// minted at CLI start. x-project-slug is the slugified working directory.
// This file replicates that identity layer so forwarded traffic carries the
// same correlation fields a real CLI run would.

// sessionTTL bounds how long one client conversation keeps its session id.
// The CLI mints a fresh session per process start; long-lived proxy
// conversations expire into a new session on the same order as a human
// restarting their terminal.
const sessionTTL = 2 * time.Hour

// newSessID mirrors the CLI's process-level session id used in
// lifecycle-events: "sess_" + 16 hex chars.
func newSessID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "sess_0000000000000000"
	}
	return "sess_" + hex.EncodeToString(b[:])
}

// newThreadID mirrors the CLI's per-conversation thread id: a uuid v4 that
// appears both as the body threadId and the x-session-id header.
func newThreadID() string { return uuid.New().String() }

// slugPath converts a working directory to the CLI's x-project-slug form:
// lowercased, every run of non-alphanumeric characters becomes a single
// dash ("D:gent\cc\web" -> "d-agent-cc-web").
func slugPath(dir string) string {
	var b strings.Builder
	prevDash := true
	for _, r := range strings.ToLower(dir) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// identity carries the per-process CLI look-alike attributes.
type identity struct {
	processSess string
	slug        string

	mu       sync.Mutex
	sessions map[string]identityEntry
}

type identityEntry struct {
	id   string
	seen time.Time
}

func newIdentity(slug string) *identity {
	if slug == "" {
		slug = "cli"
		if s := slugPath(cliWorkDir()); s != "" {
			slug = s
		}
	}
	return &identity{
		processSess: newSessID(),
		slug:        slug,
		sessions:    map[string]identityEntry{},
	}
}

// sessionFor returns the stable session id for a conversation. Preference:
// client-supplied `user` field, else a hash of model+system+first user turn
// (stable across the multi-turn replay an OpenAI client sends each round),
// falling back to the process-wide session when neither is present.
func (idn *identity) sessionFor(openAIReq api.OpenAIChatRequest) string {
	convKey := openAIReq.User
	if convKey == "" {
		system, msgs := ExtractSystem(openAIReq.Messages)
		var firstUser string
		for _, m := range msgs {
			if m.Role == "user" {
				firstUser = messageText(m)
				break
			}
		}
		if firstUser == "" && system == "" {
			// No conversation context at all: like a fresh CLI thread per
			// call — a brand-new uuid, nothing reused to correlate against.
			return newThreadID()
		}
		sum := sha256.Sum256([]byte(openAIReq.Model + "\x00" + system + "\x00" + firstUser))
		convKey = hex.EncodeToString(sum[:16])
	}

	now := time.Now()
	idn.mu.Lock()
	defer idn.mu.Unlock()
	if e, ok := idn.sessions[convKey]; ok && now.Sub(e.seen) < sessionTTL {
		e.seen = now
		idn.sessions[convKey] = e
		return e.id
	}
	for k, v := range idn.sessions {
		if now.Sub(v.seen) > sessionTTL {
			delete(idn.sessions, k)
		}
	}
	id := newThreadID()
	idn.sessions[convKey] = identityEntry{id: id, seen: now}
	return id
}

// messageText extracts a plain-text view of an OpenAI message content,
// which may be a string or a list of content parts.
func messageText(m api.OpenAIMessage) string {
	switch v := m.Content.(type) {
	case string:
		return v
	case []interface{}:
		var s string
		for _, part := range v {
			p, ok := part.(map[string]interface{})
			if !ok {
				continue
			}
			if t, ok := p["text"].(string); ok {
				s += t
			}
		}
		return s
	default:
		return ""
	}
}
