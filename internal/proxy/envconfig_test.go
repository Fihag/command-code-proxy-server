package proxy

import (
	"os"
	"path/filepath"
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
