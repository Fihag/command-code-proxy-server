package proxy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dev2k6/command-code-proxy-server/internal/api"
)

// The upstream API associates every /alpha/generate call with a CLI session:
// x-session-id, the User-Agent literal, x-project-slug and a stable
// threadId (which equals the session id for the whole lifetime of a CLI
// process). This file replicates that identity layer so forwarded traffic
// carries the same correlation fields a real CLI run would.

// sessionTTL bounds how long one client conversation keeps its session id.
// The CLI mints a fresh session per process start; long-lived proxy
// conversations expire into a new session on the same order as a human
// restarting their terminal.
const sessionTTL = 2 * time.Hour

// newSessID mirrors the CLI's generateSessionId(): "sess_" + 16 hex chars
// (the CLI uses the uuid v4 string with dashes removed, first 16 chars).
func newSessID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "sess_0000000000000000"
	}
	return "sess_" + hex.EncodeToString(b[:])
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
		if wd, err := os.Getwd(); err == nil {
			slug = filepath.Base(wd)
		} else {
			slug = "cli"
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
			return idn.processSess
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
	id := newSessID()
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
