// White-box tests for the unexported git helper: contracts that exported
// callers never exercise directly (empty argv, error text shape).
package gitx

import (
	"strings"
	"testing"
)

// TestGitEmptyArgs: an empty argument list must return an error naming the
// dir, never panic on args[0] (M1 deferred fix).
func TestGitEmptyArgs(t *testing.T) {
	dir := t.TempDir()
	out, err := git(dir)
	if err == nil {
		t.Fatalf("git with no args must error, got %q", out)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error must name the dir %q, got: %v", dir, err)
	}
}

// TestGitErrorNamesDir: a failing git call's error must carry both the dir it
// ran in and git's stderr, so per-dir failures in a batch stay diagnosable.
func TestGitErrorNamesDir(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	dir := t.TempDir() // not a repo: rev-parse fails with stderr text
	_, err := git(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil {
		t.Fatal("rev-parse in a non-repo must error")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error must name the dir %q, got: %v", dir, err)
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("error must carry git's stderr, got: %v", err)
	}
}
