package wsp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Phaengris/claude-workspaces/internal/alloc"
	"github.com/Phaengris/claude-workspaces/internal/config"
	"github.com/Phaengris/claude-workspaces/internal/gitx"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
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
	cfg := &config.Config{
		// A token-bearing value so tests can observe the resolved overlay
		// (app_T-1_dev) inside a spawned setup command.
		Env: map[string]string{"DB_NAME": "app_${WORKSPACE}_dev"},
		Projects: map[string]*config.Project{
			"app": {Repo: src, Setup: setup},
		},
	}
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

// TestEnsureProjectSetupEnvAndSubst pins the two seams of the setup step a
// refactor could silently drop while every other test stays green:
//
//   - the spawned process env is CommandEnv's CURATED slice — the resolved
//     overlay is visible ($DB_NAME), a parent-only var is NOT ($SECRET_X;
//     swapping the env for os.Environ() must fail here);
//   - the command STRING is substituted via RuntimeVars before the shell sees
//     it — ${WORKSPACE} in a setup command becomes the task id (dropping the
//     Subst call leaves the shell to expand an unset variable to "", because
//     runtime tokens are substitution inputs, never env vars).
func TestEnsureProjectSetupEnvAndSubst(t *testing.T) {
	cfg, ws := ensureFixture(t, []string{
		`printf %s "$DB_NAME" > env-probe`,
		`test -z "$SECRET_X" || printf %s leaked > secret-probe`,
		`printf %s ${WORKSPACE} > subst-probe`,
	})
	t.Setenv("SECRET_X", "leak-me-not")

	if err := wsp.EnsureProject(cfg, ws, "app"); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	dest := wsp.ProjectDir(ws, cfg, "app")
	probe := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		return string(data)
	}

	if got := probe("env-probe"); got != "app_T-1_dev" {
		t.Errorf("env-probe = %q, want %q (setup must see CommandEnv's resolved overlay)", got, "app_T-1_dev")
	}
	if got := probe("subst-probe"); got != "T-1" {
		t.Errorf("subst-probe = %q, want %q (${WORKSPACE} must be substituted in the command string)", got, "T-1")
	}
	if _, err := os.Stat(filepath.Join(dest, "secret-probe")); err == nil {
		t.Error("SECRET_X leaked into setup: the spawn env must be curated, never the parent env")
	}
}

// enclosedFixture builds the reviewer's scenario: the whole workspaces area
// sits inside SOME OTHER git repo (a dotfiles repo, a monorepo checkout, a
// home directory under version control — all ordinary), and the project's
// destination is a plain directory that already exists there. Under the old
// `is-inside-work-tree` gate the enclosing repo made dest answer "already a
// worktree", so EnsureProject skipped WorktreeAdd entirely and went straight
// on to write .env and run setup inside the enclosing repo's territory,
// stamping the result — checkout exited 0 having created no worktree at all.
// Returns cfg, ws and dest.
func enclosedFixture(t *testing.T, setup []string) (*config.Config, wsp.Workspace, string) {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	base := t.TempDir()
	enclosing := filepath.Join(base, "enclosing")
	mkRepoAt(t, enclosing, "main")
	src := filepath.Join(base, "src", "app")
	mkRepoAt(t, src, "main")

	wsDir := filepath.Join(enclosing, "workspaces", "T-1")
	dest := filepath.Join(wsDir, "app")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Projects: map[string]*config.Project{"app": {Repo: src, Setup: setup}},
	}
	return cfg, wsp.Workspace{Dir: wsDir, Alloc: alloc.Allocation{Index: 0, TaskID: "T-1"}}, dest
}

// TestEnsureProjectNonEmptyDirInsideEnclosingRepo: with a NON-EMPTY plain dir
// at dest, EnsureProject must now fail loudly — git's own "not an empty
// directory" refusal — instead of silently declaring the project checked out.
// An honest error is the whole point of the fix: the user can see and fix it.
func TestEnsureProjectNonEmptyDirInsideEnclosingRepo(t *testing.T) {
	cfg, ws, dest := enclosedFixture(t, []string{"echo ran >> setup.log"})
	if err := os.WriteFile(filepath.Join(dest, "stray"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := wsp.EnsureProject(cfg, ws, "app")
	if err == nil {
		t.Fatal("EnsureProject succeeded on a plain dir nested in an enclosing repo; it must attempt WorktreeAdd and report git's failure")
	}
	if !strings.Contains(err.Error(), `project "app"`) {
		t.Errorf("error %q does not name the project", err)
	}
	// The failure is the FIRST step, so nothing downstream may have happened:
	// no .env, no setup, no stamp claiming the work is current.
	for _, name := range []string{".env", "setup.log"} {
		if _, statErr := os.Stat(filepath.Join(dest, name)); statErr == nil {
			t.Errorf("%s written despite the worktree step failing", name)
		}
	}
	if _, statErr := os.Stat(filepath.Join(ws.Dir, ".workspace", "setup-app.ok")); statErr == nil {
		t.Error("setup stamp written despite the worktree step failing")
	}
	if gitx.IsWorkTreeRoot(dest) {
		t.Error("dest must not be a work tree root after a failed WorktreeAdd")
	}
}

// TestEnsureProjectEmptyDirInsideEnclosingRepo: the same enclosing-repo
// scenario with an EMPTY dir at dest converges instead of erroring — git
// accepts an empty directory as a worktree destination — and the result is a
// real worktree on the task branch, not the enclosing repo's checkout. This is
// the mutation guard on the fix: reverting to IsWorkTree makes this pass
// vacuously with no worktree, so the branch assertion is what pins it.
func TestEnsureProjectEmptyDirInsideEnclosingRepo(t *testing.T) {
	cfg, ws, dest := enclosedFixture(t, []string{"echo ran >> setup.log"})

	if err := wsp.EnsureProject(cfg, ws, "app"); err != nil {
		t.Fatalf("EnsureProject over an empty dir inside an enclosing repo: %v", err)
	}
	if !gitx.IsWorkTreeRoot(dest) {
		t.Fatal("dest must be a real work tree root: the enclosing repo must never stand in for the project's own worktree")
	}
	if b, err := gitx.Branch(dest); err != nil || b != "T-1" {
		t.Errorf("Branch(dest) = %q, %v; want the task branch T-1 (not the enclosing repo's)", b, err)
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
