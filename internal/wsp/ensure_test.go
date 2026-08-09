package wsp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/gitx"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

// ensureFixture builds the smallest real world EnsureProject can act on: a
// source repo on branch main and an empty workspace dir at <root>/T-1 with
// allocation index 0. SHELL is pinned to /bin/sh because proc.Run resolves it
// from the CURRENT process env and the developer's login shell may not speak
// POSIX. Setup commands assert via files, never stdout — `-lc` runs login
// init, which may write there.
func ensureFixture(t *testing.T, setup []string) (*config.Config, wsp.Workspace) {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	root := t.TempDir()
	src := filepath.Join(root, "src", "app")
	mkRepoAt(t, src, "main")
	wsDir := filepath.Join(root, "T-1")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Projects: map[string]*config.Project{
		"app": {Repo: src, Setup: setup},
	}}
	return cfg, wsp.Workspace{Dir: wsDir, Alloc: alloc.Allocation{Index: 0, TaskID: "T-1"}}
}

// setupLogLines counts the lines the fixture's append-only setup command has
// written — the observable proof of how many times setup actually ran.
func setupLogLines(t *testing.T, projectDir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(projectDir, "setup.log"))
	if err != nil {
		t.Fatalf("reading setup.log: %v", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func TestEnsureProjectIdempotent(t *testing.T) {
	cfg, ws := ensureFixture(t, []string{"echo ran >> setup.log"})

	if err := wsp.EnsureProject(cfg, ws, "app"); err != nil {
		t.Fatalf("first EnsureProject: %v", err)
	}
	dest := wsp.ProjectDir(ws, cfg, "app")

	branch, err := gitx.Branch(dest)
	if err != nil {
		t.Fatalf("reading worktree branch: %v", err)
	}
	if branch != "T-1" {
		t.Errorf("worktree branch = %q, want %q (the task id)", branch, "T-1")
	}
	if got := setupLogLines(t, dest); got != 1 {
		t.Errorf("setup.log has %d lines after first run, want 1", got)
	}
	stamp, err := os.ReadFile(filepath.Join(ws.Dir, ".workspace", "setup-app.ok"))
	if err != nil {
		t.Fatalf("reading setup stamp: %v", err)
	}
	if want := wsp.SetupHash(cfg, ws, "app") + "\n"; string(stamp) != want {
		t.Errorf("stamp = %q, want SetupHash+newline %q", stamp, want)
	}

	// Second run: worktree exists, stamp current — nothing re-runs.
	if err := wsp.EnsureProject(cfg, ws, "app"); err != nil {
		t.Fatalf("second EnsureProject: %v", err)
	}
	if got := setupLogLines(t, dest); got != 1 {
		t.Errorf("setup.log has %d lines after re-run, want 1 (setup must not re-run under a current stamp)", got)
	}
}

func TestEnsureProjectStaleStampRerunsSetup(t *testing.T) {
	cfg, ws := ensureFixture(t, []string{"echo ran >> setup.log"})
	if err := wsp.EnsureProject(cfg, ws, "app"); err != nil {
		t.Fatalf("first EnsureProject: %v", err)
	}

	// Any change to the rendered setup changes SetupHash; a trailing comment
	// changes the hash without changing what the command does.
	cfg.Projects["app"].Setup = []string{"echo ran >> setup.log # v2"}
	if err := wsp.EnsureProject(cfg, ws, "app"); err != nil {
		t.Fatalf("EnsureProject after config change: %v", err)
	}
	dest := wsp.ProjectDir(ws, cfg, "app")
	if got := setupLogLines(t, dest); got != 2 {
		t.Errorf("setup.log has %d lines after stale-stamp re-run, want 2", got)
	}
}

func TestEnsureProjectSetupFailure(t *testing.T) {
	cfg, ws := ensureFixture(t, []string{"false"})

	err := wsp.EnsureProject(cfg, ws, "app")
	if err == nil {
		t.Fatal("EnsureProject returned nil for a failing setup command")
	}
	if !strings.Contains(err.Error(), `project "app"`) {
		t.Errorf("error %q does not name the project", err)
	}
	stamp := filepath.Join(ws.Dir, ".workspace", "setup-app.ok")
	if _, statErr := os.Stat(stamp); statErr == nil {
		t.Error("setup stamp written despite a failing setup command")
	}
}

// envValue extracts K from a "K=V" slice; second return reports presence.
func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

// TestCommandEnvMergesEnvAllow pins the env_allow union and the resolved
// overlay (moved from proc when CommandEnv moved here — see ensure.go on why
// wsp, not proc, composes the spawn env).
func TestCommandEnvMergesEnvAllow(t *testing.T) {
	t.Setenv("GLOBAL_VAR", "g")
	t.Setenv("PROJ_VAR", "p")

	cfg := &config.Config{
		EnvAllow: []string{"GLOBAL_VAR"},
		Env:      map[string]string{"DB_NAME": "db_${WORKSPACE}"},
		Projects: map[string]*config.Project{
			"api": {EnvAllow: []string{"PROJ_VAR"}},
		},
	}

	env := wsp.CommandEnv(cfg, "api", "T7", 0)
	if v, ok := envValue(env, "GLOBAL_VAR"); !ok || v != "g" {
		t.Errorf("GLOBAL_VAR = %q, %v; want %q via global env_allow", v, ok, "g")
	}
	if v, ok := envValue(env, "PROJ_VAR"); !ok || v != "p" {
		t.Errorf("PROJ_VAR = %q, %v; want %q via project env_allow", v, ok, "p")
	}
	if v, ok := envValue(env, "DB_NAME"); !ok || v != "db_T7" {
		t.Errorf("DB_NAME = %q, %v; want %q from resolved overlay", v, ok, "db_T7")
	}

	env = wsp.CommandEnv(cfg, "unknown", "T7", 0)
	if v, ok := envValue(env, "GLOBAL_VAR"); !ok || v != "g" {
		t.Errorf("unknown project: GLOBAL_VAR = %q, %v; want %q (global allow stands alone)", v, ok, "g")
	}
	if _, ok := envValue(env, "PROJ_VAR"); ok {
		t.Error("unknown project: PROJ_VAR present; project env_allow must not apply")
	}
}
