package proxy

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func withWorkDirOverride(t *testing.T, dir string) {
	t.Helper()
	workDirMu.Lock()
	prev := workDirOverride
	workDirOverride = dir
	workDirMu.Unlock()
	t.Cleanup(func() {
		workDirMu.Lock()
		workDirOverride = prev
		workDirMu.Unlock()
	})
}

func TestCliWorkDirAbsoluteNormalization(t *testing.T) {
	t.Setenv("COMMANDCODE_WORKING_DIR", "") // isolate from ambient config
	withWorkDirOverride(t, ".")
	got := cliWorkDir()
	if !filepath.IsAbs(got) {
		t.Fatalf("relative override must resolve to absolute, got %q", got)
	}
	if slug := slugPath(got); slug == "" {
		t.Errorf("slug of %q is empty — header would fall back to the placeholder", got)
	}
}

func TestCliWorkDirDefaultsToHome(t *testing.T) {
	t.Setenv("COMMANDCODE_WORKING_DIR", "")
	withWorkDirOverride(t, "")
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no user home dir on this platform")
	}
	if got := cliWorkDir(); got != home {
		t.Errorf("default work dir = %q, want home %q", got, home)
	}
}

func TestEnvConfigRecentCommitsAlwaysArray(t *testing.T) {
	// Upstream validates config.recentCommits as an array: a null (nil slice
	// serialized) means 400 "expected array, received null". The real CLI's
	// buildServerConfig sends [] in exactly these two shapes — a plain
	// non-git dir, and a repo whose git log yields nothing.
	jsonOf := func(t *testing.T, dir string) string {
		t.Helper()
		t.Setenv("COMMANDCODE_WORKING_DIR", "")
		withWorkDirOverride(t, dir)
		b, err := json.Marshal(collectEnvConfig())
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	t.Run("non-git dir", func(t *testing.T) {
		got := jsonOf(t, t.TempDir())
		if !strings.Contains(got, `"recentCommits":[]`) {
			t.Errorf("recentCommits must serialize as [], got %s", got)
		}
	})
	t.Run("repo without commits", func(t *testing.T) {
		dir := t.TempDir()
		if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
		got := jsonOf(t, dir)
		if !strings.Contains(got, `"recentCommits":[]`) {
			t.Errorf("recentCommits must serialize as [], got %s", got)
		}
		if !strings.Contains(got, `"isGitRepo":true`) {
			t.Errorf("git init dir must report isGitRepo, got %s", got)
		}
	})
}

func TestSetWorkingDirSlugFollowsNormalizedDir(t *testing.T) {
	t.Setenv("COMMANDCODE_WORKING_DIR", "")
	p := NewProxy("k")
	withWorkDirOverride(t, "")
	defer func() {
		workDirMu.Lock()
		workDirOverride = ""
		workDirMu.Unlock()
	}()
	p.SetWorkingDir(".")
	cwd, err := os.Getwd()
	if err != nil {
		t.Skip(err)
	}
	want := slugPath(cwd) // independently derived: the slug must follow the ABSOLUTE dir, not "."
	if p.identity.slug != want {
		t.Errorf("identity slug = %q, want %q — slug must be derived from the normalized dir", p.identity.slug, want)
	}
}
