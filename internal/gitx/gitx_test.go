package gitx_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestGitEnvNeutralized: a process running inside a git hook inherits
// GIT_DIR (and friends) pointed at the hook's repo. gitx must strip those
// from the child env or `git -C <dir>` discovery answers about the wrong
// repo. t.Setenv poisons this test process; the functions under test must
// still answer about the real repo.
func TestGitEnvNeutralized(t *testing.T) {
	repo := mkRepo(t, "env-branch")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "bogus", ".git"))
	t.Setenv("GIT_WORK_TREE", filepath.Join(t.TempDir(), "bogus-wt"))
	if !gitx.IsWorkTree(repo) {
		t.Error("IsWorkTree must ignore inherited GIT_DIR/GIT_WORK_TREE")
	}
	if b, err := gitx.Branch(repo); err != nil || b != "env-branch" {
		t.Errorf("Branch = %q, %v; want env-branch", b, err)
	}
}

func TestBranchOnNonRepo(t *testing.T) {
	if _, err := gitx.Branch(t.TempDir()); err == nil {
		t.Error("Branch on a non-repo dir must return an error")
	}
}

// revParse resolves ref in repo via a direct exec (not the package under
// test), so worktree assertions don't depend on the code they verify.
func revParse(t *testing.T, repo, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", "--verify", ref)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse %s in %s: %v", ref, repo, err)
	}
	return strings.TrimSpace(string(out))
}

// TestWorktreeAddNewAndExistingBranch: first add creates branch T-1 from
// HEAD; after WorktreeRemove the branch survives (branches are the user's
// work), so a second add must reuse it — no `-b` collision error — and the
// branch tip must be identical, proving reuse rather than recreation.
func TestWorktreeAddNewAndExistingBranch(t *testing.T) {
	repo := mkRepo(t, "main")
	if gitx.BranchExists(repo, "T-1") {
		t.Fatal("branch T-1 must not exist in a fresh repo")
	}

	dest := filepath.Join(t.TempDir(), "wt")
	if err := gitx.WorktreeAdd(repo, dest, "T-1", ""); err != nil {
		t.Fatalf("WorktreeAdd (new branch): %v", err)
	}
	if !gitx.IsWorkTree(dest) {
		t.Error("dest must be a work tree after WorktreeAdd")
	}
	if b, err := gitx.Branch(dest); err != nil || b != "T-1" {
		t.Errorf("Branch(dest) = %q, %v; want T-1", b, err)
	}
	if !gitx.BranchExists(repo, "T-1") {
		t.Error("BranchExists must report T-1 after WorktreeAdd")
	}
	tip1 := revParse(t, repo, "refs/heads/T-1")

	if err := gitx.WorktreeRemove(repo, dest, false); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if !gitx.BranchExists(repo, "T-1") {
		t.Fatal("branch T-1 must survive worktree removal")
	}

	dest2 := filepath.Join(t.TempDir(), "wt2")
	if err := gitx.WorktreeAdd(repo, dest2, "T-1", ""); err != nil {
		t.Fatalf("WorktreeAdd (existing branch) must reuse, got: %v", err)
	}
	if b, err := gitx.Branch(dest2); err != nil || b != "T-1" {
		t.Errorf("Branch(dest2) = %q, %v; want T-1", b, err)
	}
	if tip2 := revParse(t, repo, "refs/heads/T-1"); tip2 != tip1 {
		t.Errorf("branch tip changed on reuse: %s -> %s", tip1, tip2)
	}
}

// TestWorktreeAddExplicitBase: a non-empty base must seed the new branch
// from that ref, not from HEAD.
func TestWorktreeAddExplicitBase(t *testing.T) {
	repo := mkRepo(t, "main")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Branch "dev" gets one commit main doesn't have, then HEAD returns to
	// main, so base=dev is distinguishable from base-omitted (HEAD).
	run("switch", "-c", "dev")
	if err := os.WriteFile(filepath.Join(repo, "g"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "dev commit")
	run("switch", "main")

	dest := filepath.Join(t.TempDir(), "wt")
	if err := gitx.WorktreeAdd(repo, dest, "T-2", "dev"); err != nil {
		t.Fatalf("WorktreeAdd with base: %v", err)
	}
	devTip := revParse(t, repo, "refs/heads/dev")
	mainTip := revParse(t, repo, "refs/heads/main")
	if got := revParse(t, repo, "refs/heads/T-2"); got != devTip || got == mainTip {
		t.Errorf("T-2 tip = %s; want dev tip %s (main tip %s)", got, devTip, mainTip)
	}
}

// TestWorktreeRemove: a clean worktree removes without force; one holding an
// untracked file must refuse without force and succeed with it. Either way
// the dest dir is gone afterwards.
func TestWorktreeRemove(t *testing.T) {
	repo := mkRepo(t, "main")

	clean := filepath.Join(t.TempDir(), "clean")
	if err := gitx.WorktreeAdd(repo, clean, "T-3", ""); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := gitx.WorktreeRemove(repo, clean, false); err != nil {
		t.Fatalf("WorktreeRemove (clean, no force): %v", err)
	}
	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Errorf("clean dest must be gone, stat err = %v", err)
	}

	dirty := filepath.Join(t.TempDir(), "dirty")
	if err := gitx.WorktreeAdd(repo, dirty, "T-4", ""); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirty, "junk"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gitx.WorktreeRemove(repo, dirty, false); err == nil {
		t.Error("dirty worktree must refuse removal without force")
	}
	if err := gitx.WorktreeRemove(repo, dirty, true); err != nil {
		t.Fatalf("WorktreeRemove (dirty, force): %v", err)
	}
	if _, err := os.Stat(dirty); !os.IsNotExist(err) {
		t.Errorf("forced dest must be gone, stat err = %v", err)
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
