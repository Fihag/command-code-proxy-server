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
// facts (branch, status, last commits).
//
// The snapshot directory is deliberately NOT the proxy's own cwd: launched
// from this repo it would report a project literally named
// command-code-proxy-server full of tools/tap and recapture.mjs — the config
// block would self-describe the disguise. It defaults to the user's home
// directory (a plain non-git folder, exactly like running the CLI there);
// COMMANDCODE_WORKING_DIR or Proxy.SetWorkingDir point it at any neutral
// project. x-project-slug is always derived from this same directory, so
// header and body agree.

var (
	workDirMu       sync.Mutex
	workDirOverride string // set by Proxy.SetWorkingDir before serving
)

// cliWorkDir resolves the directory the config snapshot (and slug) report.
// Relative inputs are made absolute: the CLI always reports a real cwd, and
// a literal "." would both leak through config.workingDir and collapse the
// slug to the "cli" fallback.
func cliWorkDir() string {
	workDirMu.Lock()
	ov := workDirOverride
	workDirMu.Unlock()
	dir := ov
	if dir == "" {
		dir = os.Getenv("COMMANDCODE_WORKING_DIR")
	}
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dir = home
		} else {
			dir = "."
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

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
	cwd := cliWorkDir()
	cfg := api.CCConfig{
		WorkingDir:  cwd,
		Environment: nodePlatform(),
		Structure:   topLevels(cwd),
		// The CLI's buildServerConfig always sends recentCommits as an array
		// (empty off-repo, `git log --oneline -3` split otherwise) — never
		// null. A nil slice here would serialize to "recentCommits":null and
		// the upstream schema rejects it with 400 at config.recentCommits.
		RecentCommits: []string{},
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
