package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewExitCodes pins the codes new.txtar can only assert as "non-zero"
// (spec §9). The order of checks is part of the contract: task-id validation
// (usage, 2) comes first, project validation (not found, 3) runs before any
// mutation, and a registry conflict (dup task id / dir) is a plain error (1).
func TestNewExitCodes(t *testing.T) {
	cases := map[string]struct {
		args []string
		want int
	}{
		"invalid task id":   {args: []string{"new", ".bad", "x"}, want: 2},
		"too-long task id":  {args: []string{"new", longTaskID(), "x"}, want: 2},
		"unknown project":   {args: []string{"new", "B-1", "x", "nope"}, want: 3},
		"duplicate task id": {args: []string{"new", "A-1", "different desc"}, want: 1},
		"missing args":      {args: []string{"new", "T-9"}, want: 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			registryRoot(t) // config with app/lib + allocation A-1 at <root>/A-1_x
			if got := exitCodeFor(t, tc.args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d (spec §9)", tc.args, got, tc.want)
			}
		})
	}
}

// undoHaltMessage is the exact sentence a halted undo must end with: what was
// stopped, what is still on disk, and the one command that finishes the job.
const undoHaltMessage = "undo halted: worktree removal failed; workspace dir and allocation left for retry (destroy --force after fixing the repo)"

// TestNewUndoHaltsOnWorktreeRemovalFailure pins the halt rule (M4 debt row).
// The undo stack runs LIFO — worktree, then the workspace dir, then the
// allocation — and each later step is COARSER than the one before it: removing
// the dir deletes whatever the worktree removal could not, and releasing the
// allocation forgets the workspace ever existed. So when the worktree removal
// FAILS, continuing would turn a recoverable half-state into an orphan: a
// directory git still lists as a worktree, deleted behind git's back, with no
// registry entry left to name it. The stack stops instead, and says so.
//
// Forcing the failure needs a worktree git refuses to remove while it is still
// a real worktree — a MOVED or deleted source repo cannot do it, because then
// the dest stops answering IsWorkTreeRoot and the undo skips it as a no-op.
// `git worktree lock` is exactly that state: `worktree remove --force` refuses
// a locked tree (it wants -f -f), and the tree stays perfectly valid. The lock
// is taken by the project's own setup command — the only seam between "the
// worktree exists" and "something fails" — which then exits non-zero to
// trigger the undo.
func TestNewUndoHaltsOnWorktreeRemovalFailure(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	// Hermetic git for BOTH the tool's own calls (inherited env) and the setup
	// command's (env_allow below, since the spawn env is an allowlist).
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	src := gitRepoWithCommit(t)
	cfg := fmt.Sprintf(`projects:
  app:
    repo: %s
    env_allow: [GIT_CONFIG_GLOBAL, GIT_CONFIG_SYSTEM]
    setup:
      - "git worktree lock . && exit 1"
`, src)
	root := fixtureRoot(t, map[string]string{"config.yml": cfg})

	err := runCLI(t, "new", "T-9", "x", "app")
	if err == nil {
		t.Fatal("new with a failing setup succeeded; want the setup failure")
	}
	// The original failure still leads (its kind picks the exit code), the
	// worktree removal's own error explains why the undo could not proceed,
	// and the halt sentence says what was left behind.
	for _, want := range []string{`project "app"`, "locked working tree", undoHaltMessage} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}

	// Everything after the failed step was SKIPPED: the workspace dir (with the
	// worktree still inside it) and the allocation both survive, so `destroy
	// --force` can finish the job once the repo is fixed.
	dir := filepath.Join(root, "T-9_x")
	if _, statErr := os.Stat(filepath.Join(dir, "app")); statErr != nil {
		t.Errorf("worktree dir removed after the halt: %v", statErr)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Errorf("workspace dir removed after the halt: %v (the dir undo must be skipped)", statErr)
	}
	reg, readErr := os.ReadFile(filepath.Join(root, ".allocations.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(reg), "T-9") {
		t.Errorf("allocation released after the halt: %s (the release undo must be skipped)", reg)
	}
}

// gitRepoWithCommit builds a hermetic source repo with one commit — a worktree
// can only branch from a real HEAD, so `git init` alone is not enough here (as
// it is for destroy_test.go's gitInit, which only needs a work-tree ROOT).
func gitRepoWithCommit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README")
	run("commit", "-m", "init")
	return dir
}

// longTaskID is one byte past the 64-byte limit — valid characters, invalid
// length, so it isolates the length half of ValidTaskID.
func longTaskID() string {
	b := make([]byte, 65)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
