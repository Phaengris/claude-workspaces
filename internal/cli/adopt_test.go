package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkdirIn creates <parent>/<name> and returns it, for the adopt cases whose
// whole point is WHICH directory is named.
func mkdirIn(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestAdoptExitCodes pins the codes adopt_release.txtar can only assert as
// "non-zero" (spec §9). The split is the point: an unconfigured project name
// is 3 (the identifier half that did not resolve, same rule as
// checkout/env/cd), an arg-count violation is 2 via usageArgs, and EVERY
// state refusal — missing dir, not a directory, the root itself, a basename
// that cannot be a task id, a dir already allocated — is a plain error, 1.
// Those refusals are about the world, not about how the command was typed.
func TestAdoptExitCodes(t *testing.T) {
	cases := map[string]struct {
		args func(t *testing.T, root string) []string
		want int
	}{
		"two positionals": {
			args: func(t *testing.T, root string) []string { return []string{"adopt", "a", "b"} },
			want: 2,
		},
		"unconfigured project": {
			args: func(t *testing.T, root string) []string {
				return []string{"adopt", mkdirIn(t, t.TempDir(), "F-1"), "--projects", "nope"}
			},
			want: 3,
		},
		"missing dir": {
			args: func(t *testing.T, root string) []string {
				return []string{"adopt", filepath.Join(t.TempDir(), "absent")}
			},
			want: 1,
		},
		"not a directory": {
			args: func(t *testing.T, root string) []string {
				path := filepath.Join(t.TempDir(), "F-1")
				if err := os.WriteFile(path, []byte("file"), 0o644); err != nil {
					t.Fatal(err)
				}
				return []string{"adopt", path}
			},
			want: 1,
		},
		"the workspaces root itself": {
			args: func(t *testing.T, root string) []string { return []string{"adopt", root} },
			want: 1,
		},
		"basename is not a valid task id": {
			args: func(t *testing.T, root string) []string {
				return []string{"adopt", mkdirIn(t, t.TempDir(), "x@y")}
			},
			want: 1,
		},
		"dir already allocated": {
			args: func(t *testing.T, root string) []string {
				return []string{"adopt", mkdirIn(t, root, "A-1_x")} // registryRoot's own workspace
			},
			want: 1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := registryRoot(t) // config with app/lib + allocation A-1 at <root>/A-1_x
			args := tc.args(t, root)
			if got := exitCodeFor(t, args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d (spec §9)", args, got, tc.want)
			}
		})
	}
}

// TestAdoptRootRefusalNamesTheRoot pins the message behind the root refusal:
// adopting the workspaces root would allocate the container of every
// workspace, so the refusal has to say which directory it means rather than
// fail with a generic "already allocated"-style line.
func TestAdoptRootRefusalNamesTheRoot(t *testing.T) {
	root := registryRoot(t)
	err := runCLI(t, "adopt", root)
	if err == nil {
		t.Fatal("adopting the workspaces root succeeded; it must refuse")
	}
	for _, want := range []string{"workspaces root", root} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestAdoptInvalidBasenameMessage pins that the refusal explains the ACTUAL
// problem — the directory's own name cannot be a task id — instead of echoing
// `new`'s "invalid task id" line for an id the user never typed.
func TestAdoptInvalidBasenameMessage(t *testing.T) {
	registryRoot(t)
	dir := mkdirIn(t, t.TempDir(), "x@y")
	err := runCLI(t, "adopt", dir)
	if err == nil {
		t.Fatal("adopting a dir with an unusable basename succeeded; it must refuse")
	}
	for _, want := range []string{"x@y", "task id"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestAdoptFailedWriteReleasesAllocation pins adopt's only undo. adopt writes
// into a directory it did not create, so it can never clean those files up;
// what it CAN do is not leave an allocation behind for a run that failed
// halfway. Without the release, the retry would hit the idempotent
// "already adopted" no-op and never redo the writes — the workspace would be
// permanently half-adopted.
//
// The forced failure is a --projects name whose project dir does not exist
// here, so WriteEnvFile cannot write .env.
func TestAdoptFailedWriteReleasesAllocation(t *testing.T) {
	root := registryRoot(t)
	dir := mkdirIn(t, t.TempDir(), "F-1") // no app/ inside it

	if err := runCLI(t, "adopt", dir, "--projects", "app"); err == nil {
		t.Fatal("adopt with an unwritable project .env succeeded; it must fail")
	}
	data, err := os.ReadFile(filepath.Join(root, ".allocations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), dir) {
		t.Errorf("failed adopt left an allocation behind: %s", data)
	}
}

// TestReleaseExitCodes pins release's two codes. There is no "not found" here
// on purpose: a dir with no allocation is exactly what release is FOR (the
// idempotent re-run), so it is success, 0 — the same answer whether the dir
// was never allocated, was released a moment ago, or does not exist at all.
func TestReleaseExitCodes(t *testing.T) {
	cases := map[string]struct {
		args func(t *testing.T, root string) []string
		want int
	}{
		"two positionals": {
			args: func(t *testing.T, root string) []string { return []string{"release", "a", "b"} },
			want: 2,
		},
		"dir with no allocation": {
			args: func(t *testing.T, root string) []string {
				return []string{"release", mkdirIn(t, t.TempDir(), "F-1")}
			},
			want: 0,
		},
		"dir that does not exist": {
			args: func(t *testing.T, root string) []string {
				return []string{"release", filepath.Join(t.TempDir(), "absent")}
			},
			want: 0,
		},
		"allocated workspace": {
			args: func(t *testing.T, root string) []string {
				return []string{"release", filepath.Join(root, "A-1_x")}
			},
			want: 0,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := registryRoot(t)
			args := tc.args(t, root)
			if got := exitCodeFor(t, args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d (spec §9)", args, got, tc.want)
			}
		})
	}
}

// TestReleaseNeverTouchesDisk is the half of release's contract that no exit
// code can express: the allocation goes, the directory and everything in it
// stays. It is the escape hatch for "stop tracking this", not a delete.
func TestReleaseNeverTouchesDisk(t *testing.T) {
	root := registryRoot(t)
	dir := filepath.Join(root, "A-1_x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(dir, "work.txt")
	if err := os.WriteFile(canary, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := runCLI(t, "release", dir); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(canary); err != nil {
		t.Errorf("release removed %s: %v", canary, err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".allocations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "A-1") {
		t.Errorf("release did not drop the allocation: %s", data)
	}
}
