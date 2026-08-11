package wsp_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Phaengris/claude-workspaces/internal/alloc"
	"github.com/Phaengris/claude-workspaces/internal/config"
	"github.com/Phaengris/claude-workspaces/internal/proc"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
	"github.com/Phaengris/claude-workspaces/internal/xerr"
)

// daemonCfg is the fixture the daemon/target tests share. Two deliberate
// shapes:
//
//   - app depends on web, so TopoOrder is web,app — the REVERSE of
//     alphabetical, which is what makes the ordering assertions real rather
//     than accidentally satisfied by map-key sorting;
//   - "worker" is a daemon of BOTH projects (the collision case) while
//     "rails"/"vite" are unique, and app has a daemon named "web" — the same
//     string as a configured PROJECT, which pins the precedence rule.
//
// Each project also carries run-and-wait entries in non-adjacent positions, so
// ordering assertions cover interleaving.
func daemonCfg() *config.Config {
	return &config.Config{
		Projects: map[string]*config.Project{
			"app": {Repo: "/r", Depends: config.StringList{"web"}, Start: []config.StartEntry{
				{Cmd: "bundle install"},
				{Name: "rails", Cmd: "rails s -p ${PORT0}"},
				{Cmd: "bin/rails db:prepare"},
				{Name: "worker", Cmd: "sidekiq"},
				{Name: "web", Cmd: "webpack -w"},
			}},
			"web": {Repo: "/w", Path: "www", Start: []config.StartEntry{
				{Name: "vite", Cmd: "vite dev"},
				{Name: "worker", Cmd: "node worker.js"},
				{Cmd: "npm ci"},
			}},
		},
	}
}

func TestDaemonsOfAndRunAndWaits(t *testing.T) {
	cfg := daemonCfg()

	var got []string
	for _, d := range wsp.DaemonsOf(cfg, "app") {
		got = append(got, d.Name+"="+d.Cmd)
	}
	want := "rails=rails s -p ${PORT0},worker=sidekiq,web=webpack -w"
	if strings.Join(got, ",") != want {
		t.Errorf("DaemonsOf(app) = %v, want %s (named entries, listed order)", got, want)
	}
	if ds := wsp.DaemonsOf(cfg, "app"); len(ds) == 0 || ds[0].Project != "app" {
		t.Errorf("DaemonsOf must stamp the owning project, got %+v", ds)
	}

	if rw := wsp.RunAndWaits(cfg, "app"); strings.Join(rw, ",") != "bundle install,bin/rails db:prepare" {
		t.Errorf("RunAndWaits(app) = %v, want the bare entries in listed order", rw)
	}
	if rw := wsp.RunAndWaits(cfg, "web"); strings.Join(rw, ",") != "npm ci" {
		t.Errorf("RunAndWaits(web) = %v, want [npm ci]", rw)
	}

	// An unconfigured project has neither — callers must not have to check.
	if ds := wsp.DaemonsOf(cfg, "ghost"); len(ds) != 0 {
		t.Errorf("DaemonsOf(ghost) = %v, want none", ds)
	}
	if rw := wsp.RunAndWaits(cfg, "ghost"); len(rw) != 0 {
		t.Errorf("RunAndWaits(ghost) = %v, want none", rw)
	}
}

func TestDaemonKeyAndPaths(t *testing.T) {
	ws := wsp.Workspace{Dir: "/ws/T-1"}
	d := wsp.Daemon{Project: "app", Name: "rails", Cmd: "rails s"}

	if d.Key() != "app:rails" {
		t.Errorf("Key = %q, want app:rails", d.Key())
	}
	cases := []struct{ got, want string }{
		{wsp.PidPath(ws, d), "/ws/T-1/.workspace/pids/app:rails"},
		{wsp.LogPath(ws, d), "/ws/T-1/.workspace/logs/app:rails.log"},
		{wsp.ErrLogPath(ws, d), "/ws/T-1/.workspace/logs/app:rails.err.log"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("path = %q, want %q", tc.got, tc.want)
		}
	}
}

// TestDaemonState drives the derived-state doctrine against real pid files:
// nothing is recorded but `<pid> <starttime>`, and running-ness is that pair
// still naming a live process.
func TestDaemonState(t *testing.T) {
	ws := wsp.Workspace{Dir: t.TempDir()}
	d := wsp.Daemon{Project: "app", Name: "rails"}
	pidPath := wsp.PidPath(ws, d)
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(pidPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// No pid file at all: stopped, no pid.
	if running, pid := wsp.DaemonState(ws, d); running || pid != 0 {
		t.Errorf("missing pid file = (%v, %d), want (false, 0)", running, pid)
	}

	// A pid file naming THIS test process with its real starttime: running.
	self := os.Getpid()
	st, err := proc.Starttime(self)
	if err != nil {
		st = 0 // no /proc: the documented pid-only degradation
	}
	write(strconv.Itoa(self) + " " + strconv.FormatUint(st, 10) + "\n")
	if running, pid := wsp.DaemonState(ws, d); !running || pid != self {
		t.Errorf("live pid file = (%v, %d), want (true, %d)", running, pid, self)
	}

	// Same live pid, WRONG starttime — the pid-recycling case: not running.
	if st != 0 {
		write(strconv.Itoa(self) + " 1\n")
		if running, pid := wsp.DaemonState(ws, d); running || pid != 0 {
			t.Errorf("recycled pid = (%v, %d), want (false, 0)", running, pid)
		}
	}

	// A pid that has been freed: run a real process to completion, then claim
	// it in the pid file.
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	write(strconv.Itoa(cmd.Process.Pid) + " 12345\n")
	if running, pid := wsp.DaemonState(ws, d); running || pid != 0 {
		t.Errorf("freed pid = (%v, %d), want (false, 0)", running, pid)
	}

	// Corrupt content is not an error upstream: it reads as stopped, and `up`
	// is free to overwrite it.
	write("garbage\n")
	if running, pid := wsp.DaemonState(ws, d); running || pid != 0 {
		t.Errorf("corrupt pid file = (%v, %d), want (false, 0)", running, pid)
	}
}

// fmtWork renders a work list compactly so the resolution table can compare
// whole results — project order, whole-vs-partial, and daemon order at once.
func fmtWork(work []wsp.TargetWork) string {
	var parts []string
	for _, w := range work {
		names := make([]string, len(w.Daemons))
		for i, d := range w.Daemons {
			names[i] = d.Name
		}
		kind := "daemons"
		if w.WholeProject {
			kind = "whole"
		}
		parts = append(parts, w.Project+"["+kind+":"+strings.Join(names, ",")+"]")
	}
	return strings.Join(parts, " ")
}

func TestResolveTargets(t *testing.T) {
	cfg := daemonCfg()
	// Nothing is checked out here on purpose: resolution is a CONFIG-level
	// question (`up` from cold must be able to name a daemon before any
	// worktree exists).
	ws := wsp.Workspace{Dir: t.TempDir(), Alloc: alloc.Allocation{TaskID: "T-1"}}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"whole project", []string{"app"}, "app[whole:rails,worker,web]"},
		{"explicit pair", []string{"app:rails"}, "app[daemons:rails]"},
		{"bare unique daemon, project not checked out", []string{"vite"}, "web[daemons:vite]"},
		{"project name beats daemon name", []string{"web"}, "web[whole:vite,worker]"},
		{"two projects in topo order", []string{"app", "web"}, "web[whole:vite,worker] app[whole:rails,worker,web]"},
		{"whole project absorbs its daemon", []string{"app:rails", "app"}, "app[whole:rails,worker,web]"},
		{"daemons merge in listed order, deduped", []string{"app:worker", "app:rails", "app:worker"}, "app[daemons:rails,worker]"},
		{"cross-project daemons grouped and ordered", []string{"app:rails", "vite"}, "web[daemons:vite] app[daemons:rails]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work, err := wsp.ResolveTargets(cfg, ws, tc.args)
			if err != nil {
				t.Fatalf("ResolveTargets(%v) = %v", tc.args, err)
			}
			if got := fmtWork(work); got != tc.want {
				t.Errorf("ResolveTargets(%v) = %s, want %s", tc.args, got, tc.want)
			}
		})
	}
}

func TestResolveTargetsErrors(t *testing.T) {
	cfg := daemonCfg()
	ws := wsp.Workspace{Dir: t.TempDir()}

	cases := []struct {
		name     string
		args     []string
		kind     error    // nil means "a plain error", exit code 1
		contains []string // substrings the message must carry
	}{
		{"unknown bare name", []string{"nope"}, xerr.ErrNotFound, []string{"nope"}},
		{"unknown project in pair", []string{"ghost:rails"}, xerr.ErrNotFound, []string{"ghost"}},
		{"unknown daemon in pair", []string{"app:nope"}, xerr.ErrNotFound, []string{"app", "nope"}},
		{"run-and-wait is not addressable", []string{"app:bundle install"}, xerr.ErrNotFound, []string{"app"}},
		{"empty daemon half", []string{"app:"}, xerr.ErrUsage, []string{"app:"}},
		{"empty project half", []string{":rails"}, xerr.ErrUsage, []string{":rails"}},
		{"too many colons", []string{"a:b:c"}, xerr.ErrUsage, []string{"a:b:c"}},
		// A colliding BARE name is not "not found" — the name did match, and
		// picking one project arbitrarily could act on the wrong daemon.
		{"ambiguous bare daemon", []string{"worker"}, nil, []string{"app:worker", "web:worker"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := wsp.ResolveTargets(cfg, ws, tc.args)
			if err == nil {
				t.Fatalf("ResolveTargets(%v) must fail", tc.args)
			}
			if tc.kind != nil && !errors.Is(err, tc.kind) {
				t.Errorf("error = %v, want kind %v", err, tc.kind)
			}
			if tc.kind == nil {
				for _, kind := range []error{xerr.ErrNotFound, xerr.ErrUsage, xerr.ErrConfig} {
					if errors.Is(err, kind) {
						t.Errorf("error = %v, want a plain error, got kind %v", err, kind)
					}
				}
			}
			for _, want := range tc.contains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %v must mention %q", err, want)
				}
			}
		})
	}
}

// TestResolveTargetsAmbiguousCandidatesSorted pins the candidate list order:
// error text is compared by humans across runs, so it must not follow map
// iteration.
func TestResolveTargetsAmbiguousCandidatesSorted(t *testing.T) {
	cfg := daemonCfg()
	cfg.Projects["zeta"] = &config.Project{Repo: "/z", Start: []config.StartEntry{{Name: "worker", Cmd: "z"}}}
	_, err := wsp.ResolveTargets(cfg, wsp.Workspace{Dir: t.TempDir()}, []string{"worker"})
	if err == nil {
		t.Fatal("want ambiguity error")
	}
	if !strings.Contains(err.Error(), "app:worker, web:worker, zeta:worker") {
		t.Errorf("candidates must be listed sorted by project, got %v", err)
	}
}

// TestResolveTargetsNoArgs: no targets means the whole workspace — every
// CHECKED-OUT project, in topo order. Both projects are real worktrees here.
func TestResolveTargetsNoArgs(t *testing.T) {
	cfg := daemonCfg()
	wsDir := t.TempDir()
	mkRepoAt(t, filepath.Join(wsDir, "app"), "T-1")
	mkRepoAt(t, filepath.Join(wsDir, "www"), "T-1") // web's `path`
	ws := wsp.Workspace{Dir: wsDir, Alloc: alloc.Allocation{TaskID: "T-1"}}

	work, err := wsp.ResolveTargets(cfg, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmtWork(work), "web[whole:vite,worker] app[whole:rails,worker,web]"; got != want {
		t.Errorf("no-args = %s, want %s", got, want)
	}
}

// TestResolveTargetsNoArgsOnlyCheckedOut: a configured but absent project is
// not part of "the whole workspace" — a workspace is a subset of the config.
func TestResolveTargetsNoArgsOnlyCheckedOut(t *testing.T) {
	cfg := daemonCfg()
	wsDir := t.TempDir()
	mkRepoAt(t, filepath.Join(wsDir, "app"), "T-1")
	ws := wsp.Workspace{Dir: wsDir, Alloc: alloc.Allocation{TaskID: "T-1"}}

	work, err := wsp.ResolveTargets(cfg, ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmtWork(work), "app[whole:rails,worker,web]"; got != want {
		t.Errorf("no-args = %s, want %s", got, want)
	}
}

// TestResolveTargetsNoArgsNothingCheckedOut: an empty result, NOT an error —
// the caller decides whether "nothing to do" deserves a message.
func TestResolveTargetsNoArgsNothingCheckedOut(t *testing.T) {
	work, err := wsp.ResolveTargets(daemonCfg(), wsp.Workspace{Dir: t.TempDir()}, nil)
	if err != nil {
		t.Fatalf("empty workspace must not error, got %v", err)
	}
	if len(work) != 0 {
		t.Errorf("work = %v, want empty", work)
	}
}
