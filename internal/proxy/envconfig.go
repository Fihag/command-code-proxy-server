package proxy

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dev2k6/command-code-proxy-server/internal/api"
)

// The CLI attaches a per-request env snapshot (buildServerConfig in
// command-code dist/cli.mjs) to every /alpha/generate call: the real cwd,
// a sorted top-level directory listing with noise entries removed, and git
// facts (branch, status, last commits). We collect the same data from the
// proxy process's own working directory so the snapshot is self-consistent
// with x-project-slug instead of placeholder values.

// structureNoise mirrors the CLI's _w exclusion set.
var structureNoise = map[string]bool{
	"node_modules": true, "dist": true, "build": true, ".git": true,
	".svn": true, ".hg": true, "coverage": true, ".nyc_output": true,
	".cache": true, "tmp": true, "temp": true, ".next": true, ".nuxt": true,
	"out": true,
}

const envConfigTTL = 60 * time.Second

type envConfigCache struct {
	mu     sync.Mutex
	cfg    api.CCConfig
	got    time.Time
	gather func() api.CCConfig // injectable for tests
}

func newEnvConfigCache() *envConfigCache {
	c := &envConfigCache{}
	c.gather = collectEnvConfig
	return c
}

// get returns the cached snapshot, refreshing date (daily boundary) and all
// collected fields once the TTL expires.
func (c *envConfigCache) get() api.CCConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.got) > envConfigTTL {
		c.cfg = c.gather()
		c.got = time.Now()
	}
	cfg := c.cfg
	cfg.Date = time.Now().UTC().Format("2006-01-02")
	return cfg
}

// nodePlatform maps Go's GOOS to Node's process.platform string, which is
// what the CLI sends as config.environment.
func nodePlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	default:
		return runtime.GOOS
	}
}

func collectGit(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Match the CLI's shellOutput: stdout on success, empty on any failure.
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func collectEnvConfig() api.CCConfig {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	cfg := api.CCConfig{
		WorkingDir:  cwd,
		Environment: nodePlatform(),
		Structure:   topLevels(cwd),
	}
	if collectGit(cwd, "rev-parse", "--git-dir") == "" {
		return cfg // IsGitRepo false, empty branch/status — same as CLI
	}
	cfg.IsGitRepo = true
	cfg.CurrentBranch = collectGit(cwd, "branch", "--show-current")
	cfg.MainBranch = resolveMainBranch(cwd)
	if st := collectGit(cwd, "status", "--porcelain"); st != "" {
		cfg.GitStatus = st
	} else {
		cfg.GitStatus = "Working tree clean"
	}
	if lg := collectGit(cwd, "log", "--oneline", "-3"); lg != "" {
		cfg.RecentCommits = strings.Split(lg, "\n")
	}
	return cfg
}

// resolveMainBranch mirrors the CLI: origin/HEAD symbolic ref first, else
// look for origin/main or origin/master among remote branches, else "main".
func resolveMainBranch(dir string) string {
	if ref := collectGit(dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); ref != "" {
		return strings.TrimPrefix(ref, "origin/")
	}
	remotes := collectGit(dir, "branch", "-r")
	if strings.Contains(remotes, "origin/main") {
		return "main"
	}
	if strings.Contains(remotes, "origin/master") {
		return "master"
	}
	return "main"
}

func topLevels(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") || structureNoise[n] {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	if names == nil {
		names = []string{}
	}
	return names
}

// projectSlugFor derives the x-project-slug the same way the CLI does:
// the base name of the working directory.
func projectSlugFor(dir string) string {
	return filepath.Base(dir)
}
