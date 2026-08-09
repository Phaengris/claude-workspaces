package gitx_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/gitx"
)

// mkRepo builds a real repo in a temp dir, hermetically sealed against the
// host's git configuration: the user's ~/.gitconfig or the system config may
// set init.defaultBranch, commit signing, core.hooksPath, status tweaks, etc.
// Both config scopes are pointed at /dev/null via t.Setenv, which covers not
// just this helper's git calls but also the gitx functions under test (they
// inherit the test process env). `git init -b` pins the branch name
// explicitly on top of that, so the test never depends on any default.
func mkRepo(t *testing.T, branch string) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", branch)
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	return dir
}

func TestIsWorkTreeAndBranch(t *testing.T) {
	repo := mkRepo(t, "feature-x")
	if !gitx.IsWorkTree(repo) {
		t.Error("real repo must be a worktree")
	}
	if gitx.IsWorkTree(t.TempDir()) {
		t.Error("plain dir must not be a worktree")
	}
	if gitx.IsWorkTree(filepath.Join(t.TempDir(), "missing")) {
		t.Error("missing dir must not be a worktree")
	}
	if b, err := gitx.Branch(repo); err != nil || b != "feature-x" {
		t.Errorf("Branch = %q, %v; want feature-x", b, err)
	}
}

func TestBranchOnNonRepo(t *testing.T) {
	if _, err := gitx.Branch(t.TempDir()); err == nil {
		t.Error("Branch on a non-repo dir must return an error")
	}
}

func TestStatsForConcurrent(t *testing.T) {
	clean := mkRepo(t, "main")
	dirty := mkRepo(t, "dev")
	if err := os.WriteFile(filepath.Join(dirty, "new"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "gone")

	stats := gitx.StatsFor([]string{clean, dirty, missing}, 2)
	if len(stats) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(stats), stats)
	}
	if s := stats[clean]; s.Err != nil || s.Branch != "main" || s.Dirty {
		t.Errorf("clean: %+v", s)
	}
	if s := stats[dirty]; s.Err != nil || s.Branch != "dev" || !s.Dirty {
		t.Errorf("dirty: %+v", s)
	}
	if stats[missing].Err == nil {
		t.Error("missing dir must record an error, not abort the batch")
	}
}

// TestStatsForLimitClamp: limit < 1 must be treated as 1, not deadlock or
// panic (a zero-capacity semaphore channel would block forever).
func TestStatsForLimitClamp(t *testing.T) {
	repo := mkRepo(t, "main")
	for _, limit := range []int{0, -3} {
		stats := gitx.StatsFor([]string{repo}, limit)
		if s := stats[repo]; s.Err != nil || s.Branch != "main" {
			t.Errorf("limit=%d: %+v", limit, s)
		}
	}
}

// TestStatsForMoreDirsThanLimit exercises the semaphore with contention:
// more dirs than allowed goroutines, run under -race to shake out unsynced
// writes to the shared result map.
func TestStatsForMoreDirsThanLimit(t *testing.T) {
	repos := map[string]string{
		mkRepo(t, "a"): "a",
		mkRepo(t, "b"): "b",
		mkRepo(t, "c"): "c",
		mkRepo(t, "d"): "d",
		mkRepo(t, "e"): "e",
	}
	dirs := make([]string, 0, len(repos))
	for dir := range repos {
		dirs = append(dirs, dir)
	}
	stats := gitx.StatsFor(dirs, 2)
	if len(stats) != len(repos) {
		t.Fatalf("got %d entries, want %d", len(stats), len(repos))
	}
	for dir, branch := range repos {
		if s := stats[dir]; s.Err != nil || s.Branch != branch || s.Dirty {
			t.Errorf("%s: %+v, want branch %q clean", dir, s, branch)
		}
	}
}

func TestStatsForEmpty(t *testing.T) {
	if stats := gitx.StatsFor(nil, 4); len(stats) != 0 {
		t.Errorf("empty input must yield empty map, got %+v", stats)
	}
}
