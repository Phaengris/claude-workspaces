package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// validConfig is the smallest config the query commands accept: one value
// block, no projects.
const validConfig = "values:\n  PORT: { start: 5000, per_workspace: 10 }\n"

// fixtureRoot writes the given files (name → content) into a fresh root and
// points $CLAUDE_WORKSPACES_ROOT_DIR at it for the duration of the test.
func fixtureRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CLAUDE_WORKSPACES_ROOT_DIR", root)
	return root
}

// TestQueryExitCodes pins the exit codes the txtar script can only assert as
// "non-zero" (spec §9): a config problem is 4 (the kind Load attaches, passed
// through untouched), an unreadable registry is a plain error → 1, and an empty
// registry is not an error at all → 0. Every command that opens with loadRoot
// and can run without a workspace argument is covered, `status` included —
// identifier-specific codes live in TestStatusEnvExitCodes.
func TestQueryExitCodes(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		want  int
	}{
		"missing config":  {files: nil, want: 4},
		"invalid config":  {files: map[string]string{"config.yml": "values:\n  PORT: { start: -1, per_workspace: 0 }\n"}, want: 4},
		"broken registry": {files: map[string]string{"config.yml": validConfig, ".allocations.json": "{ not json"}, want: 1},
		"empty registry":  {files: map[string]string{"config.yml": validConfig}, want: 0},
	}
	for name, tc := range cases {
		for _, sub := range []string{"ls", "ports", "status"} {
			t.Run(name+"/"+sub, func(t *testing.T) {
				fixtureRoot(t, tc.files)
				if got := exitCodeFor(t, sub); got != tc.want {
					t.Errorf("%s exit code = %d, want %d", sub, got, tc.want)
				}
			})
		}
	}
}

// TestLsRowFormat pins the human row shape independently of tabwriter padding:
// the index cell carries the '#' prefix, and -g adds one PROJECT@BRANCH cell per
// checked-out project with '*' marking a dirty tree. `status` reuses lsRow, so
// this is a shared contract, not just ls's.
func TestLsRowFormat(t *testing.T) {
	e := lsEntry{Name: "A-1_x", Index: 0, TaskID: "A-1", Description: "fix the thing"}
	got := lsRow(e)
	want := []string{"A-1_x", "#0", "A-1", "fix the thing"}
	if len(got) != len(want) {
		t.Fatalf("lsRow = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cell %d = %q, want %q", i, got[i], want[i])
		}
	}

	projects := []lsProject{{Name: "app", Branch: "main"}, {Name: "lib", Branch: "wip", Dirty: true}}
	e.Projects = &projects
	got = lsRow(e)
	if len(got) != 6 || got[4] != "app@main" || got[5] != "lib@wip*" {
		t.Errorf("lsRow with -g = %q, want project cells app@main, lib@wip*", got)
	}

	// -g on a workspace with nothing checked out adds no cells (and JSON still
	// carries an empty array — that is the pointer field's whole purpose).
	empty := []lsProject{}
	e.Projects = &empty
	if got := lsRow(e); len(got) != 4 {
		t.Errorf("lsRow with -g and no projects = %q, want 4 cells", got)
	}
}

// TestLsRowAdoptedMarker pins how adoption shows up in human output: appended
// to the DESCRIPTION cell, not as a fifth column — the -g project cells start
// at index 4 and must keep doing so, and `status` reuses this same row. An
// adopted workspace has no description of its own (adopt takes none), so the
// common case is the marker alone.
func TestLsRowAdoptedMarker(t *testing.T) {
	cases := map[string]struct {
		entry lsEntry
		want  string
	}{
		"adopted, no description":   {entry: lsEntry{Adopted: true}, want: "(adopted)"},
		"adopted, with description": {entry: lsEntry{Description: "side work", Adopted: true}, want: "side work (adopted)"},
		"not adopted":               {entry: lsEntry{Description: "side work"}, want: "side work"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := lsRow(tc.entry)
			if len(got) != 4 {
				t.Fatalf("lsRow = %q, want 4 cells", got)
			}
			if got[3] != tc.want {
				t.Errorf("description cell = %q, want %q", got[3], tc.want)
			}
		})
	}
}
