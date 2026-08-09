// Package gitx is read-only git plumbing. Every invocation is argv form —
// exec.Command("git", "-C", dir, ...) — no shell ever touches a repo path
// (spec §7). The package is a leaf: it consumes nothing internal.
package gitx

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// git runs one git command in dir and returns trimmed stdout. stdout and
// stderr are kept separate on purpose: git can emit warnings on stderr even
// when it succeeds, and mixing them into the parsed output would corrupt
// results (e.g. a clean `status --porcelain` misread as dirty). On failure
// the stderr text is folded into the returned error so per-dir failures stay
// diagnosable.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, msg)
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
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

// Branch returns the current branch name of the work tree at dir
// (or "HEAD" when detached).
func Branch(dir string) (string, error) {
	return git(dir, "rev-parse", "--abbrev-ref", "HEAD")
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
func statsOne(dir string) Stats {
	branch, err := Branch(dir)
	if err != nil {
		return Stats{Err: err}
	}
	porcelain, err := git(dir, "status", "--porcelain")
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
