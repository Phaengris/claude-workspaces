// Package gitx is git plumbing (reads and worktree writes). Every invocation
// is argv form — exec.Command("git", "-C", dir, ...) — no shell ever touches
// a repo path (spec §7). The package is a leaf: it consumes nothing internal.
package gitx

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// gitEnvDenied are the repo-location variables git sets for hook processes.
// A caller running inside a git hook inherits them pointed at the hook's
// repo; passed through they would override `-C <dir>` discovery and every
// gitx answer would be about the wrong repo. They are stripped from every
// child env. Config-scope variables (GIT_CONFIG_GLOBAL/SYSTEM etc.) are
// deliberately kept: they don't redirect discovery, and tests rely on them
// for hermeticity.
var gitEnvDenied = map[string]bool{
	"GIT_DIR":              true,
	"GIT_WORK_TREE":        true,
	"GIT_INDEX_FILE":       true,
	"GIT_COMMON_DIR":       true,
	"GIT_OBJECT_DIRECTORY": true,
}

// neutralEnv returns os.Environ() minus the gitEnvDenied variables.
func neutralEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		name, _, _ := strings.Cut(kv, "=")
		if !gitEnvDenied[name] {
			out = append(out, kv)
		}
	}
	return out
}

// git runs one git command in dir and returns trimmed stdout. The child env
// is neutralEnv(), so running from inside a git hook cannot poison `-C`
// discovery. stdout and stderr are kept separate on purpose: git can emit
// warnings on stderr even when it succeeds, and mixing them into the parsed
// output would corrupt results (e.g. a clean `status --porcelain` misread as
// dirty). On failure the returned error names the dir and folds in git's
// stderr so per-dir failures stay diagnosable. An empty args list is an
// error, never a panic.
func git(dir string, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("gitx: no git arguments (dir %s)", dir)
	}
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = neutralEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git -C %s %s: %w: %s", dir, args[0], err, msg)
		}
		return "", fmt.Errorf("git -C %s %s: %w", dir, args[0], err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// IsWorkTree reports whether dir is inside a git work tree. Any git failure
// (missing dir, not a repo, bare repo) is simply "no" — read-only callers
// never need to distinguish why.
func IsWorkTree(dir string) bool {
	out, err := git(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// IsWorkTreeRoot reports whether dir is the TOP of a git work tree — the
// directory a checkout actually lives at, not merely a directory somewhere
// underneath one.
//
// This is the predicate "is this project checked out here?" needs.
// IsWorkTree's question (`--is-inside-work-tree`) walks UP: if the workspaces
// area happens to sit inside any enclosing repo — a dotfiles repo, a monorepo
// checkout, a version-controlled home directory — then EVERY plain directory
// under it answers "true", and a caller reading that as "already a worktree"
// silently skips creating one while still writing .env, running setup and
// stamping the result inside the enclosing repo's territory.
//
// The implementation asks git for the top level and requires it to BE dir.
// Both sides go through filepath.Clean only: `--show-toplevel` prints a
// PHYSICAL path (symlinks resolved) while the caller's path may be logical,
// so an EvalSymlinks on the local side would be the principled next step —
// deliberately not done, because no test in this tree needs it and resolving
// links changes which directory the answer is about. If a symlinked
// workspaces root ever surfaces, that is the place to fix it. As with
// IsWorkTree, any git failure is simply "no".
func IsWorkTreeRoot(dir string) bool {
	top, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	return filepath.Clean(top) == filepath.Clean(dir)
}

// Branch returns the current branch name of the work tree at dir
// (or "HEAD" when detached).
func Branch(dir string) (string, error) {
	return git(dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// BranchExists reports whether refs/heads/<branch> exists in the repo at
// repo. Any git failure (missing dir, not a repo) is simply "no".
func BranchExists(repo, branch string) bool {
	_, err := git(repo, "rev-parse", "--verify", "refs/heads/"+branch)
	return err == nil
}

// WorktreeAdd checks out branch as a linked worktree at dest. If the branch
// already exists it is reused as-is (`worktree add <dest> <branch>` — the
// branch is the user's work, never recreated); otherwise the branch is
// created from base (`worktree add -b <branch> <dest> <base>`), or from the
// repo's HEAD when base is empty. dest must not already exist.
func WorktreeAdd(repo, dest, branch, base string) error {
	if BranchExists(repo, branch) {
		_, err := git(repo, "worktree", "add", dest, branch)
		return err
	}
	args := []string{"worktree", "add", "-b", branch, dest}
	if base != "" {
		args = append(args, base)
	}
	_, err := git(repo, args...)
	return err
}

// WorktreeRemove removes the linked worktree at dest. Without force git
// refuses when the worktree holds modified or untracked files; force
// discards them. The branch the worktree had checked out is never deleted.
func WorktreeRemove(repo, dest string, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, dest)
	_, err := git(repo, args...)
	return err
}

// Stats is one dir's git summary. Err records a per-dir failure so a batch
// never aborts on one bad dir; when Err is non-nil the other fields may be
// partial (Branch is kept if only the dirty check failed).
type Stats struct {
	Branch string
	Dirty  bool
	Err    error
}

// statsOne computes Stats for a single dir: branch first, then dirtiness
// from `status --porcelain` (any output means dirty).
// --untracked-files=normal pins the dirty definition regardless of the
// host's status.showUntrackedFiles config.
func statsOne(dir string) Stats {
	branch, err := Branch(dir)
	if err != nil {
		return Stats{Err: err}
	}
	porcelain, err := git(dir, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return Stats{Branch: branch, Err: err}
	}
	return Stats{Branch: branch, Dirty: porcelain != ""}
}

// StatsFor computes Stats for every dir concurrently, with at most limit
// dirs being processed at once (limit < 1 is treated as 1). The bound is a
// buffered-channel semaphore — the raw pattern errgroup wraps — and the
// mutex guards the shared result map, because maps are not safe for
// concurrent writes. Per-dir errors land in Stats.Err; the batch never
// aborts. The result is keyed by dir, one entry per distinct input dir.
func StatsFor(dirs []string, limit int) map[string]Stats {
	if limit < 1 {
		limit = 1
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, limit)
		out = make(map[string]Stats, len(dirs))
	)
	for _, dir := range dirs {
		wg.Add(1)
		// Go ≥1.22: loop variables are per-iteration, so the closure
		// captures this iteration's dir — no shadowing copy needed
		// (the classic pre-1.22 goroutine-in-loop bug).
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s := statsOne(dir)
			mu.Lock()
			out[dir] = s
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}
