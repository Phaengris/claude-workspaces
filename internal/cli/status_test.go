package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Phaengris/claude-workspaces/internal/alloc"
	"github.com/Phaengris/claude-workspaces/internal/config"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
)

// projectConfig is a valid config with two projects, used by the tests that
// need `env`'s project overlay and `status`'s configured-project list.
const projectConfig = `values:
  PORT: { start: 5000, per_workspace: 10 }
env:
  DB_NAME: global_${WORKSPACE}
projects:
  app:
    repo: /tmp/app-src
    env:
      DB_NAME: ${PROJECT}_${WORKSPACE}_dev
  lib:
    repo: /tmp/lib-src
`

// registryRoot builds a root holding projectConfig plus one allocation whose
// dir is <root>/A-1_x — the registry keys on absolute paths, so the file can
// only be written once the temp root is known.
func registryRoot(t *testing.T) string {
	t.Helper()
	root := fixtureRoot(t, map[string]string{"config.yml": projectConfig})
	reg := `{"` + filepath.Join(root, "A-1_x") + `": {"index": 0, "task_id": "A-1", ` +
		`"description": "fix the thing", "created_at": "2026-08-01T09:00:00Z", "adopted": false}}`
	if err := os.WriteFile(filepath.Join(root, ".allocations.json"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestStatusEnvExitCodes pins the codes txtar can only assert as "non-zero"
// (spec §9). Every unresolvable-identifier case here is 3: spec §9 promises
// exit 3 for "workspace/project not found", and that covers an unconfigured
// project exactly as it covers an unknown workspace — the workspace resolving
// successfully first doesn't change what the project half of the identifier
// means. This is what makes `workspace status X` usable as a shell test for
// existence.
func TestStatusEnvExitCodes(t *testing.T) {
	cases := map[string]struct {
		args []string
		want int
	}{
		"status unknown workspace": {args: []string{"status", "A-1"}, want: 3},
		"env unknown workspace":    {args: []string{"env", "A-1"}, want: 3},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fixtureRoot(t, map[string]string{"config.yml": validConfig})
			if got := exitCodeFor(t, tc.args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d", tc.args, got, tc.want)
			}
		})
	}

	// Against a root that really holds the workspace: resolvable identifiers
	// succeed, an unconfigured project is exit 3 too (spec §9).
	withRegistry := map[string]struct {
		args []string
		want int
	}{
		"status found":             {args: []string{"status", "A-1"}, want: 0},
		"status found by name":     {args: []string{"status", "A-1_x"}, want: 0},
		"env found":                {args: []string{"env", "A-1"}, want: 0},
		"env with project":         {args: []string{"env", "A-1", "app"}, want: 0},
		"env unconfigured project": {args: []string{"env", "A-1", "nope"}, want: 3},
		// Resolution happens before project validation, so an unknown workspace
		// keeps its own code even when the project is bogus too.
		"env unknown workspace and project": {args: []string{"env", "nope", "nope"}, want: 3},
	}
	for name, tc := range withRegistry {
		t.Run(name, func(t *testing.T) {
			registryRoot(t)
			if got := exitCodeFor(t, tc.args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d", tc.args, got, tc.want)
			}
		})
	}

	// Arg-count violations are usage errors (exit 2), via usageArgs.
	for name, args := range map[string][]string{
		"status two args": {"status", "a", "b"},
		"env no args":     {"env"},
		"env three args":  {"env", "a", "b", "c"},
	} {
		t.Run(name, func(t *testing.T) {
			registryRoot(t)
			if got := exitCodeFor(t, args...); got != 2 {
				t.Errorf("%v exit code = %d, want 2 (usage error, spec §9)", args, got)
			}
		})
	}
}

// TestStatusTaskLine pins the em-dash contract and its one edge: a workspace
// with no description prints the bare task id, never a dangling separator.
func TestStatusTaskLine(t *testing.T) {
	if got, want := statusTaskLine("A-1", "fix the thing"), "A-1 — fix the thing"; got != want {
		t.Errorf("statusTaskLine = %q, want %q", got, want)
	}
	if got, want := statusTaskLine("A-1", ""), "A-1"; got != want {
		t.Errorf("statusTaskLine with empty description = %q, want %q", got, want)
	}
}

// TestStatusProjectDetail pins every project-line wording, including the two
// degraded readings: an unreadable branch renders as "unknown" (not as an empty
// parenthetical), and a missing stamp is worded exactly like a mismatched one.
func TestStatusProjectDetail(t *testing.T) {
	cases := map[string]struct {
		p    statusProject
		want string
	}{
		"checked out, setup current": {
			p:    statusProject{CheckedOut: true, Branch: "main", SetupCurrent: true},
			want: "checked out (branch main), setup current",
		},
		"checked out, setup stale": {
			p:    statusProject{CheckedOut: true, Branch: "main"},
			want: "checked out (branch main), setup stale",
		},
		"branch unreadable": {
			p:    statusProject{CheckedOut: true, SetupCurrent: true},
			want: "checked out (branch unknown), setup current",
		},
		"not checked out": {
			p:    statusProject{Name: "lib"},
			want: "not checked out",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := statusProjectDetail(tc.p); got != tc.want {
				t.Errorf("statusProjectDetail = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStatusDaemonDetail pins the two daemon-line wordings. A stopped daemon
// shows no pid at all: DaemonState reports 0 for every not-running reason
// (missing, stale, recycled record), and printing "(pid 0)" would read as a
// real process.
func TestStatusDaemonDetail(t *testing.T) {
	if got, want := statusDaemonDetail(statusDaemon{Running: true, Pid: 4242}), "running (pid 4242)"; got != want {
		t.Errorf("statusDaemonDetail(running) = %q, want %q", got, want)
	}
	if got, want := statusDaemonDetail(statusDaemon{}), "stopped"; got != want {
		t.Errorf("statusDaemonDetail(stopped) = %q, want %q", got, want)
	}
}

// TestStatusOfDaemons pins WHICH projects carry daemon state: a checked-out
// project always does (empty list when it configures none — the key is present
// so a --json consumer never has to distinguish "no daemons" from "not
// measured"), a project that is not checked out never does (nil → no lines, no
// key), matching the existing one-line convention for absent projects.
//
// Daemon ORDER is listed order, not sorted: it is start order (spec §7), and
// re-sorting would hide the sequence `up` actually follows. Run-and-waits (bare
// start entries) are not daemons and must not appear.
func TestStatusOfDaemons(t *testing.T) {
	cfg := &config.Config{Projects: map[string]*config.Project{
		"app": {Repo: "/tmp/app-src", Start: []config.StartEntry{
			{Cmd: "touch prestart"},
			{Name: "web", Cmd: "sleep 30"},
			{Name: "api", Cmd: "sleep 30"},
		}},
		"quiet": {Repo: "/tmp/quiet-src"},
		"lib":   {Repo: "/tmp/lib-src", Start: []config.StartEntry{{Name: "liblet", Cmd: "sleep 30"}}},
	}}
	dir := t.TempDir()
	ws := wsp.Workspace{Dir: dir, Alloc: alloc.Allocation{Index: 0, TaskID: "A-1"}}
	// app and quiet are checked out (real work trees); lib is not.
	gitInit(t, filepath.Join(dir, "app"))
	gitInit(t, filepath.Join(dir, "quiet"))

	byName := map[string]statusProject{}
	for _, p := range statusOf(cfg, ws).Projects {
		byName[p.Name] = p
	}

	app := byName["app"]
	if app.Daemons == nil {
		t.Fatalf("app = %+v, want a daemons list (checked out)", app)
	}
	var got []string
	for _, d := range *app.Daemons {
		if d.Running || d.Pid != 0 {
			t.Errorf("daemon %+v reported running; nothing was ever started", d)
		}
		got = append(got, d.Name)
	}
	if want := []string{"web", "api"}; !slices.Equal(got, want) {
		t.Errorf("app daemons = %v, want %v (listed order, run-and-waits excluded)", got, want)
	}

	if q := byName["quiet"]; q.Daemons == nil || len(*q.Daemons) != 0 {
		t.Errorf("quiet = %+v, want an EMPTY daemons list (checked out, none configured)", q)
	}
	if lib := byName["lib"]; lib.Daemons != nil {
		t.Errorf("lib = %+v, want no daemons list (not checked out)", lib)
	}
}

// TestStatusOfListsEveryConfiguredProject pins the difference between `status`
// and `ls -g`: wsp.ProjectStates reports only checked-out projects, so status
// fills in the remaining configured ones itself. The temp workspace dir holds
// no repos at all, so every project must come back not-checked-out — with its
// prospective Dir still filled in, so a caller can act on the path.
func TestStatusOfListsEveryConfiguredProject(t *testing.T) {
	cfg := &config.Config{Projects: map[string]*config.Project{
		"lib": {Repo: "/tmp/lib-src"},
		"app": {Repo: "/tmp/app-src", Path: "web"},
	}}
	dir := t.TempDir()
	ws := wsp.Workspace{Dir: dir, Alloc: alloc.Allocation{Index: 3, TaskID: "A-1"}}

	entry := statusOf(cfg, ws)
	if entry.Name != filepath.Base(dir) || entry.Index != 3 || entry.TaskID != "A-1" {
		t.Errorf("header fields = %+v, want name %q index 3 task A-1", entry, filepath.Base(dir))
	}
	if len(entry.Projects) != 2 {
		t.Fatalf("projects = %+v, want 2 (every configured project)", entry.Projects)
	}
	if entry.Projects[0].Name != "app" || entry.Projects[1].Name != "lib" {
		t.Errorf("projects not sorted by name: %+v", entry.Projects)
	}
	// `path: web` is honored even for an absent project.
	if want := filepath.Join(dir, "web"); entry.Projects[0].Dir != want {
		t.Errorf("app dir = %q, want %q", entry.Projects[0].Dir, want)
	}
	for _, p := range entry.Projects {
		if p.CheckedOut || p.Branch != "" || p.SetupCurrent {
			t.Errorf("project %q = %+v, want zero state (nothing checked out)", p.Name, p)
		}
	}
}
