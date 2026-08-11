package wsp_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Phaengris/claude-workspaces/internal/alloc"
	"github.com/Phaengris/claude-workspaces/internal/config"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
	"github.com/Phaengris/claude-workspaces/internal/xerr"
)

// mkRepoAt builds a real repo at dir (creating it), hermetically sealed
// against the host's git configuration: the user's ~/.gitconfig or the system
// config may set init.defaultBranch, commit signing, core.hooksPath, status
// tweaks, etc. Both config scopes are pointed at /dev/null via t.Setenv, which
// covers not just this helper's git calls but also the gitx functions the code
// under test calls (they inherit the test process env). `git init -b` pins the
// branch name explicitly on top of that, so the test never depends on any
// default. Every git invocation is argv form — no shell.
//
// This is a deliberate duplicate of gitx_test.mkRepo: test helpers in an
// external test package (gitx_test) are not importable, and a shared testutil
// package for two callers would cost more than the twenty lines it saves.
func mkRepoAt(t *testing.T, dir, branch string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
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
}

func TestWorkspaceName(t *testing.T) {
	ws := wsp.Workspace{Dir: "/ws/T-1_thing"}
	if ws.Name() != "T-1_thing" {
		t.Errorf("Name = %q, want T-1_thing", ws.Name())
	}
}

func TestListSortedAndResolve(t *testing.T) {
	reg := alloc.Registry{
		"/ws/B-2_two": {Index: 1, TaskID: "B-2"},
		"/ws/A-1_one": {Index: 0, TaskID: "A-1"},
	}
	l := wsp.List(reg)
	if len(l) != 2 || l[0].Name() != "A-1_one" || l[1].Name() != "B-2_two" {
		t.Fatalf("List = %v", l)
	}
	if l[0].Alloc.TaskID != "A-1" || l[1].Alloc.Index != 1 {
		t.Errorf("List must carry allocations: %+v", l)
	}
	if ws, err := wsp.Resolve(reg, "A-1_one"); err != nil || ws.Dir != "/ws/A-1_one" {
		t.Errorf("by name: %v %v", ws, err)
	}
	if ws, err := wsp.Resolve(reg, "B-2"); err != nil || ws.Dir != "/ws/B-2_two" {
		t.Errorf("by task id: %v %v", ws, err)
	}
	_, err := wsp.Resolve(reg, "NOPE-9")
	if !errors.Is(err, xerr.ErrNotFound) {
		t.Errorf("unknown ident must be ErrNotFound, got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "NOPE-9") {
		t.Errorf("not-found error must name the ident, got %v", err)
	}
}

func TestListEmpty(t *testing.T) {
	if l := wsp.List(alloc.Registry{}); len(l) != 0 {
		t.Errorf("empty registry must list nothing, got %v", l)
	}
	if _, err := wsp.Resolve(alloc.Registry{}, "T-1"); !errors.Is(err, xerr.ErrNotFound) {
		t.Errorf("empty registry: want ErrNotFound, got %v", err)
	}
}

// TestResolveNameBeatsTaskID: a full name always wins, even when the same
// string is another workspace's task id — otherwise `status <name>` could
// silently act on a different workspace.
func TestResolveNameBeatsTaskID(t *testing.T) {
	reg := alloc.Registry{
		"/ws/T-1":       {Index: 0, TaskID: "X-9"},
		"/ws/T-1_other": {Index: 1, TaskID: "T-1"},
	}
	ws, err := wsp.Resolve(reg, "T-1")
	if err != nil || ws.Dir != "/ws/T-1" {
		t.Errorf("Resolve = %+v, %v; want the exact-name match /ws/T-1", ws, err)
	}
}

func TestResolveAmbiguousTaskID(t *testing.T) {
	reg := alloc.Registry{
		"/ws/T-1_a": {Index: 0, TaskID: "T-1"},
		"/ws/T-1_b": {Index: 1, TaskID: "T-1"},
	}
	_, err := wsp.Resolve(reg, "T-1")
	if err == nil || errors.Is(err, xerr.ErrNotFound) {
		t.Fatalf("ambiguous task id must be a distinct error, got %v", err)
	}
	for _, name := range []string{"T-1_a", "T-1_b"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("ambiguity error should list %q, got %v", name, err)
		}
	}
}

func TestProjectDir(t *testing.T) {
	cfg := testCfg()
	ws := wsp.Workspace{Dir: "/ws/T-1"}
	cases := []struct{ project, want string }{
		{"app", "/ws/T-1/app"},     // no path: name is the dir
		{"web", "/ws/T-1/www"},     // path override
		{"ghost", "/ws/T-1/ghost"}, // unconfigured: fall back to the name
	}
	for _, tc := range cases {
		if got := wsp.ProjectDir(ws, cfg, tc.project); got != tc.want {
			t.Errorf("ProjectDir(%q) = %q, want %q", tc.project, got, tc.want)
		}
	}
}

func TestSetupHashRendersCommands(t *testing.T) {
	cfg := testCfg()
	cfg.Projects["app"].Setup = []string{"echo ${WORKSPACE}", "make ${PORT0}"}
	ws := wsp.Workspace{Dir: "/ws/T-1", Alloc: alloc.Allocation{Index: 1, TaskID: "T-1"}}

	want := sha256.Sum256([]byte("echo T-1\nmake 5002"))
	if got := wsp.SetupHash(cfg, ws, "app"); got != hex.EncodeToString(want[:]) {
		t.Errorf("SetupHash = %s, want %s", got, hex.EncodeToString(want[:]))
	}
	// A config change must change the hash — that is the whole point of the
	// stamp (spec §3): M2's `up` re-runs setup on mismatch.
	before := wsp.SetupHash(cfg, ws, "app")
	cfg.Projects["app"].Setup = []string{"echo ${WORKSPACE}", "make ${PORT0}", "true"}
	if after := wsp.SetupHash(cfg, ws, "app"); after == before {
		t.Error("changing setup commands must change the hash")
	}
	// The index feeds the rendering, so two workspaces differ.
	ws2 := wsp.Workspace{Dir: "/ws/T-2", Alloc: alloc.Allocation{Index: 2, TaskID: "T-2"}}
	if wsp.SetupHash(cfg, ws2, "app") == wsp.SetupHash(cfg, ws, "app") {
		t.Error("different workspaces must render to different hashes")
	}
	// No setup and an unknown project both hash the empty string.
	empty := sha256.Sum256(nil)
	if got := wsp.SetupHash(cfg, ws, "nosuch"); got != hex.EncodeToString(empty[:]) {
		t.Errorf("unknown project SetupHash = %s, want the empty hash", got)
	}
}

func TestProjectStates(t *testing.T) {
	wsDir := t.TempDir()
	cfg := testCfg()
	cfg.Projects["app"].Setup = []string{"echo ${WORKSPACE}", "make ${PORT0}"}
	ws := wsp.Workspace{Dir: wsDir, Alloc: alloc.Allocation{Index: 1, TaskID: "T-1"}}

	// "app" is checked out: a real git work tree right where ProjectDir says.
	mkRepoAt(t, filepath.Join(wsDir, "app"), "fix-1")
	// "web" (path "www") exists but is NOT a git work tree — the harder
	// negative case than a missing dir.
	if err := os.MkdirAll(filepath.Join(wsDir, "www"), 0o755); err != nil {
		t.Fatal(err)
	}

	states := wsp.ProjectStates(cfg, ws)
	if len(states) != 1 {
		t.Fatalf("states = %+v; want only the checked-out project", states)
	}
	got := states[0]
	if got.Name != "app" || got.Dir != filepath.Join(wsDir, "app") || !got.CheckedOut {
		t.Errorf("state = %+v", got)
	}
	if got.Branch != "fix-1" {
		t.Errorf("Branch = %q, want fix-1", got.Branch)
	}
	if got.SetupCurrent {
		t.Error("no stamp yet: SetupCurrent must be false")
	}

	// A stamp holding the hash (trailing newline and all) means current.
	stampDir := filepath.Join(wsDir, ".workspace")
	if err := os.MkdirAll(stampDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(stampDir, "setup-app.ok")
	if err := os.WriteFile(stamp, []byte(wsp.SetupHash(cfg, ws, "app")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if states = wsp.ProjectStates(cfg, ws); !states[0].SetupCurrent {
		t.Error("stamp matching hash must mean SetupCurrent")
	}

	// A stale stamp (config changed since setup ran) must not be current.
	if err := os.WriteFile(stamp, []byte("deadbeef\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if states = wsp.ProjectStates(cfg, ws); states[0].SetupCurrent {
		t.Error("stamp not matching hash must mean stale")
	}
}

// TestProjectStatesSorted: several checked-out projects come back ordered by
// name, so `ls`/`status` output is stable regardless of map iteration.
func TestProjectStatesSorted(t *testing.T) {
	wsDir := t.TempDir()
	cfg := testCfg()
	for _, name := range []string{"zeta", "mid", "alpha"} {
		cfg.Projects[name] = &config.Project{Repo: "/r/" + name}
		mkRepoAt(t, filepath.Join(wsDir, name), "main")
	}
	ws := wsp.Workspace{Dir: wsDir, Alloc: alloc.Allocation{TaskID: "T-1"}}

	var names []string
	for _, s := range wsp.ProjectStates(cfg, ws) {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "alpha,mid,zeta" {
		t.Errorf("names = %v, want sorted alpha,mid,zeta", names)
	}
}

// TestProjectStatesIgnoresEnclosingRepo: when the workspaces area itself lives
// inside some other git repo, a plain project directory is "inside a work
// tree" — the enclosing one — and the old predicate reported it as checked
// out, so `status` claimed a project with an unknown branch and `destroy`
// would have run its teardown. Checked-out means THIS dir is the top of a work
// tree, nothing less.
func TestProjectStatesIgnoresEnclosingRepo(t *testing.T) {
	base := t.TempDir()
	enclosing := filepath.Join(base, "enclosing")
	mkRepoAt(t, enclosing, "main")

	wsDir := filepath.Join(enclosing, "workspaces", "T-1")
	if err := os.MkdirAll(filepath.Join(wsDir, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := wsp.Workspace{Dir: wsDir, Alloc: alloc.Allocation{TaskID: "T-1"}}

	if states := wsp.ProjectStates(testCfg(), ws); len(states) != 0 {
		t.Errorf("states = %+v; a plain dir inside an enclosing repo is not checked out", states)
	}
}

func TestProjectStatesNoneCheckedOut(t *testing.T) {
	if states := wsp.ProjectStates(testCfg(), wsp.Workspace{Dir: t.TempDir()}); len(states) != 0 {
		t.Errorf("empty workspace dir must yield no states, got %+v", states)
	}
}
