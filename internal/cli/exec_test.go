package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// execConfig has one configured project that is never checked out, which is
// enough to pin exec's error split: identifier problems are 3, runtime
// problems (not checked out, command missing) are 1, arg-shape problems are 2.
const execConfig = `values:
  PORT: { start: 5000, per_workspace: 10 }
env:
  DB_NAME: app_${WORKSPACE}_dev
projects:
  app:
    repo: /tmp/app-src
`

// execRoot builds a root holding execConfig plus one allocation at
// <root>/A-1_x. Nothing is checked out and nothing needs to be: only failure
// paths run here — a SUCCESSFUL exec would replace the test process itself
// (unix.Exec), so every happy path lives in exec.txtar where the workspace
// command is already a subprocess.
func execRoot(t *testing.T) string {
	t.Helper()
	root := fixtureRoot(t, map[string]string{"config.yml": execConfig})
	reg := `{"` + filepath.Join(root, "A-1_x") + `": {"index": 0, "task_id": "A-1", ` +
		`"description": "d", "created_at": "2026-08-01T09:00:00Z", "adopted": false}}`
	if err := os.WriteFile(filepath.Join(root, ".allocations.json"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestExecExitCodes pins the codes exec.txtar can only assert as "non-zero"
// (spec §9). Unknown workspace is 3; a configured project that is not checked
// out HERE and a command the curated PATH cannot find are both plain 1 (the
// decided row: runtime failures, not identifier ones); arg-shape mistakes —
// too few args, a sniffed project with no command left — are usage, 2.
func TestExecExitCodes(t *testing.T) {
	cases := map[string]struct {
		args []string
		want int
	}{
		"unknown workspace":       {args: []string{"exec", "NOPE-9", "pwd"}, want: 3},
		"project not checked out": {args: []string{"exec", "A-1", "app", "pwd"}, want: 1},
		"command not found":       {args: []string{"exec", "A-1", "no-such-cmd-xyz-42"}, want: 1},
		"project but no command":  {args: []string{"exec", "A-1", "app"}, want: 2},
		"no args":                 {args: []string{"exec"}, want: 2},
		"workspace only":          {args: []string{"exec", "A-1"}, want: 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			execRoot(t)
			if got := exitCodeFor(t, tc.args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d (spec §9)", tc.args, got, tc.want)
			}
		})
	}
}

// TestLookPathIn pins the curated-PATH search: first match wins in segment
// order, a command containing a separator bypasses the search entirely (the
// exec syscall reports its own error), non-executables and empty segments are
// skipped, and no match is an error naming the command.
func TestLookPathIn(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	writeFile := func(dir, name string, mode os.FileMode) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	both1 := writeFile(dir1, "tool", 0o755)
	writeFile(dir2, "tool", 0o755)
	only2 := writeFile(dir2, "only2", 0o755)
	writeFile(dir1, "noexec", 0o644)

	pathVar := dir1 + ":" + dir2
	cases := map[string]struct {
		pathVar string
		cmd     string
		want    string // "" = expect an error
	}{
		"first dir wins":           {pathVar: pathVar, cmd: "tool", want: both1},
		"found in second dir":      {pathVar: pathVar, cmd: "only2", want: only2},
		"not found":                {pathVar: pathVar, cmd: "absent"},
		"absolute passthrough":     {pathVar: pathVar, cmd: "/no/such/file", want: "/no/such/file"},
		"relative passthrough":     {pathVar: pathVar, cmd: "./x", want: "./x"},
		"non-executable skipped":   {pathVar: dir1, cmd: "noexec"},
		"empty PATH finds nothing": {pathVar: "", cmd: "tool"},
		"empty segments skipped":   {pathVar: "::", cmd: "tool"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := lookPathIn(tc.pathVar, tc.cmd)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("lookPathIn(%q, %q) = %q, want error", tc.pathVar, tc.cmd, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("lookPathIn(%q, %q): %v", tc.pathVar, tc.cmd, err)
			}
			if got != tc.want {
				t.Errorf("lookPathIn(%q, %q) = %q, want %q", tc.pathVar, tc.cmd, got, tc.want)
			}
		})
	}
}
