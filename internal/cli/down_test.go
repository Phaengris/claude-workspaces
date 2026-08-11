package cli

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Phaengris/claude-workspaces/internal/alloc"
	"github.com/Phaengris/claude-workspaces/internal/proc"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
	"github.com/Phaengris/claude-workspaces/internal/xerr"
)

// TestDownRestartExitCodes pins the codes down_restart.txtar can only assert
// as "non-zero" (spec §9): unresolvable identifiers (workspace or target) are
// 3, a missing workspace argument is a usage error 2, and converging an empty
// workspace (nothing checked out) is a no-op SUCCESS for both commands — the
// same ensure doctrine `up` pins. The registryRoot fixture's workspace dir
// does not exist on disk, so `A-1` with no targets resolves to an empty work
// list.
func TestDownRestartExitCodes(t *testing.T) {
	cases := map[string]struct {
		args []string
		want int
	}{
		"down unknown workspace":      {args: []string{"down", "NOPE-9"}, want: 3},
		"down unknown target":         {args: []string{"down", "A-1", "nope"}, want: 3},
		"down no args":                {args: []string{"down"}, want: 2},
		"down nothing checked out":    {args: []string{"down", "A-1"}, want: 0},
		"restart unknown workspace":   {args: []string{"restart", "NOPE-9"}, want: 3},
		"restart unknown target":      {args: []string{"restart", "A-1", "nope"}, want: 3},
		"restart no args":             {args: []string{"restart"}, want: 2},
		"restart nothing checked out": {args: []string{"restart", "A-1"}, want: 0},
		// The alias must classify identically to the canonical name.
		"alias stop, unknown target": {args: []string{"stop", "A-1", "nope"}, want: 3},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			registryRoot(t)
			if got := exitCodeFor(t, tc.args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d (spec §9)", tc.args, got, tc.want)
			}
		})
	}
}

// TestDownReverseOrder pins BOTH levels of down's reverse-order contract (the
// decided Order row) without any processes: web depends on api, so up order is
// api-then-web and down must walk web-then-api; within each project, daemons
// listed one-then-two must be visited two-then-one. No pid files exist, so
// every daemon reads `already stopped` — the LINE ORDER is the assertion, and
// an accidental forward loop at either level reorders it. The exact-match also
// pins Finding 2's quiet branch: neither project has stop: commands, so the
// missing worktrees produce no skip notes.
func TestDownReverseOrder(t *testing.T) {
	const orderConfig = `values:
  PORT: { start: 5000, per_workspace: 10 }
projects:
  api:
    repo: /tmp/api-src
    start:
      - one: sleep 30
      - two: sleep 30
  web:
    repo: /tmp/web-src
    depends: api
    start:
      - one: sleep 30
      - two: sleep 30
`
	root := fixtureRoot(t, map[string]string{"config.yml": orderConfig})
	reg := `{"` + filepath.Join(root, "A-1_x") + `": {"index": 0, "task_id": "A-1", ` +
		`"description": "d", "created_at": "2026-08-01T09:00:00Z", "adopted": false}}`
	if err := os.WriteFile(filepath.Join(root, ".allocations.json"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := Root()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	// Targets in FORWARD dependency order, so any reversal in the output is
	// the code's doing (ResolveTargets orders by topo regardless of args).
	cmd.SetArgs([]string{"down", "A-1", "api", "web"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("down: %v", err)
	}

	want := []string{
		"web:two already stopped",
		"web:one already stopped",
		"api:two already stopped",
		"api:one already stopped",
	}
	got := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(got) != len(want) {
		t.Fatalf("down printed %d lines, want %d:\n%s", len(got), len(want), out.String())
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q (reverse-order contract)", i, got[i], want[i])
		}
	}
}

// unlistedFixture builds a root with one allocated workspace A-1 whose dir
// EXISTS on disk (so its pids directory can hold records), one configured
// project `api` with a single daemon `one`, and the given pid-file records
// (key → content) already written. No project is checked out — the config-driven
// work list is therefore empty, which is exactly the situation where the pids
// directory is the only inventory there is.
func unlistedFixture(t *testing.T, records map[string]string) (root string, ws wsp.Workspace) {
	t.Helper()
	const cfg = `values:
  PORT: { start: 5000, per_workspace: 10 }
projects:
  api:
    repo: /tmp/api-src
    start:
      - one: sleep 30
`
	root = fixtureRoot(t, map[string]string{"config.yml": cfg})
	ws = wsp.Workspace{
		Dir:   filepath.Join(root, "A-1_x"),
		Alloc: alloc.Allocation{TaskID: "A-1", Description: "d", Index: 0},
	}
	if err := os.MkdirAll(wsp.PidsDir(ws), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := `{"` + ws.Dir + `": {"index": 0, "task_id": "A-1", ` +
		`"description": "d", "created_at": "2026-08-01T09:00:00Z", "adopted": false}}`
	if err := os.WriteFile(filepath.Join(root, ".allocations.json"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
	for key, content := range records {
		if err := os.WriteFile(filepath.Join(wsp.PidsDir(ws), key), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, ws
}

// startRecord starts a REAL daemon in ws under the given key and returns its
// (pid, starttime) — the only honest way to exercise the LIVE half of the
// enumeration: a hand-written record can only ever be dead or corrupt, and
// pointing a live record at some borrowed pid risks aiming StopGroup at this
// very test process. The command is `sleep 30`, so nothing can outlive the run
// even if the stop under test fails, and the cleanup stops the group anyway.
func startRecord(t *testing.T, ws wsp.Workspace, key string) (int, uint64) {
	t.Helper()
	project, name, _ := strings.Cut(key, ":")
	d := wsp.Daemon{Project: project, Name: name}
	for _, dir := range []string{wsp.PidsDir(ws), filepath.Dir(wsp.LogPath(ws, d))} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// PATH only: StartDaemon takes the TOTAL env, and `sleep` has to be findable.
	env := []string{"PATH=" + os.Getenv("PATH")}
	if err := proc.StartDaemon(ws.Dir, "sleep 30", env,
		wsp.LogPath(ws, d), wsp.ErrLogPath(ws, d), wsp.PidPath(ws, d)); err != nil {
		t.Fatal(err)
	}
	pid, starttime, err := proc.ReadPidFile(wsp.PidPath(ws, d))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = proc.StopGroup(pid, starttime) })
	if !proc.Alive(pid, starttime) {
		t.Fatalf("daemon recorded as %s died before the test began", key)
	}
	return pid, starttime
}

// downLines runs the given command and returns its stdout lines.
func downLines(t *testing.T, args ...string) []string {
	t.Helper()
	cmd := Root()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// TestDownEnumeratesPidsDir pins the decided "down unlisted keys" row: a
// no-explicit-target `down` (and `restart`'s down half) takes its inventory from
// the pids DIRECTORY, not the config, because a pid file is named after the key
// `up` wrote it under — rename a daemon, drop it from `start:`, or remove its
// project, and the record (and the process it names, still holding this index's
// ports) becomes invisible to every config-driven walk. `down <ws>` promises the
// workspace is stopped afterwards, and only the directory can make that promise
// good.
//
// The row is scoped to LIVE keys, and the subtests pin both halves of that: a
// live unlisted daemon is stopped and its record removed; a dead or corrupt one
// is passed over in silence and left for gc.
func TestDownEnumeratesPidsDir(t *testing.T) {
	// pid 1 exists (kill(1,0) is EPERM at worst) but its starttime cannot be
	// this, so the record reads dead — no signal is ever sent.
	const dead = "1 99999999999\n"

	t.Run("a LIVE unlisted key is stopped and its record removed", func(t *testing.T) {
		_, ws := unlistedFixture(t, nil)
		pid, starttime := startRecord(t, ws, "gone:ghost")
		got := downLines(t, "down", "A-1")
		want := []string{"stopped gone:ghost (TERM)"}
		if !slices.Equal(got, want) {
			t.Errorf("down A-1 printed %q, want %q (pids-dir enumeration)", got, want)
		}
		if proc.Alive(pid, starttime) {
			t.Error("the unlisted daemon is still running after down")
		}
		if _, err := os.Stat(filepath.Join(wsp.PidsDir(ws), "gone:ghost")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("pid file survived a confirmed stop: %v", err)
		}
	})

	t.Run("dead and corrupt unlisted records stay silent", func(t *testing.T) {
		_, ws := unlistedFixture(t, map[string]string{"gone:ghost": dead, "gone:corrupt": "garbage\n"})
		got := downLines(t, "down", "A-1")
		// Nothing was addressed, so the hint is the only honest output — and a
		// stray file in the directory must never earn an `already stopped` line.
		if len(got) != 1 || !strings.Contains(got[0], "nothing checked out") {
			t.Errorf("down A-1 printed %q, want just the checkout hint", got)
		}
		for _, key := range []string{"gone:ghost", "gone:corrupt"} {
			if _, err := os.Stat(filepath.Join(wsp.PidsDir(ws), key)); err != nil {
				t.Errorf("%s was removed; reaping stale records is gc's job: %v", key, err)
			}
		}
	})

	t.Run("an EXPLICIT target never enumerates", func(t *testing.T) {
		_, ws := unlistedFixture(t, nil)
		pid, starttime := startRecord(t, ws, "gone:ghost")
		// The grammar is unchanged: naming `api` acts on api's daemons and
		// nothing else, however much else the directory records — and a LIVE
		// record makes that assertion bite (a dead one would be silent anyway).
		got := downLines(t, "down", "A-1", "api")
		want := []string{"api:one already stopped"}
		if !slices.Equal(got, want) {
			t.Errorf("down A-1 api printed %q, want %q (explicit targets never enumerate)", got, want)
		}
		if !proc.Alive(pid, starttime) {
			t.Error("a named target stopped an unlisted daemon nobody named")
		}
		if _, err := os.Stat(filepath.Join(wsp.PidsDir(ws), "gone:ghost")); err != nil {
			t.Errorf("an unnamed daemon's record was removed: %v", err)
		}
	})

	t.Run("empty pids dir keeps the hint", func(t *testing.T) {
		unlistedFixture(t, nil)
		got := downLines(t, "down", "A-1")
		if len(got) != 1 || !strings.Contains(got[0], "nothing checked out") {
			t.Errorf("down A-1 printed %q, want just the checkout hint", got)
		}
	})

	t.Run("restart's down half enumerates too", func(t *testing.T) {
		_, ws := unlistedFixture(t, nil)
		pid, starttime := startRecord(t, ws, "gone:ghost")
		got := downLines(t, "restart", "A-1")
		if !slices.Contains(got, "stopped gone:ghost (TERM)") {
			t.Errorf("restart A-1 printed %q, want the unlisted daemon stopped", got)
		}
		if proc.Alive(pid, starttime) {
			t.Error("restart left the unlisted daemon running")
		}
		// Nothing config-known is checked out, so the up half starts nothing —
		// and it could not start `gone:ghost` in any case (see newRestartCmd).
		for _, line := range got {
			if strings.HasPrefix(line, "started ") {
				t.Errorf("restart started %q; the up half can only start config-known daemons", line)
			}
		}
	})
}

// TestDownUnlistedSkipsCoveredKeys pins the dedupe guard, and it is pinned HERE,
// on the helper, with a LIVE record — the only shape in which the guard is
// observable. Once dead records are silent, a stale covered key looks identical
// with and without the guard, and a live covered key is normally gone from the
// directory by the time the enumeration lists it (downWork removed the record on
// a confirmed stop). What remains is the case the guard exists for: a record
// downWork OWNS that is still live and still recorded — a stop that failed, or
// one that raced — where enumerating it again would signal the same process a
// second time in one run and report the same daemon twice. downUnlisted is
// therefore called directly with a work list that covers the key, which is
// exactly the state downWork leaves behind.
func TestDownUnlistedSkipsCoveredKeys(t *testing.T) {
	_, ws := unlistedFixture(t, nil)
	pid, starttime := startRecord(t, ws, "api:one")

	work := []wsp.TargetWork{{
		Project:      "api",
		WholeProject: true,
		Daemons:      []wsp.Daemon{{Project: "api", Name: "one", Cmd: "sleep 30"}},
	}}
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	addressed, err := downUnlisted(cmd, ws, work)
	if err != nil {
		t.Fatalf("downUnlisted: %v", err)
	}
	if addressed != 0 {
		t.Errorf("downUnlisted addressed %d records, want 0: api:one belongs to the work list", addressed)
	}
	if out.String() != "" {
		t.Errorf("downUnlisted printed %q for a covered key; downWork already reported it", out.String())
	}
	if !proc.Alive(pid, starttime) {
		t.Error("downUnlisted stopped a daemon the work list owns — the same process would be signaled twice in one down")
	}
	if _, err := os.Stat(wsp.PidPath(ws, wsp.Daemon{Project: "api", Name: "one"})); err != nil {
		t.Errorf("downUnlisted removed a covered record: %v", err)
	}
}

// TestDownRecordsUnreadable pins the refuse-on-doubt half of the enumeration: a
// pids directory that cannot be LISTED leaves what is running unknown, so it is
// an error rather than an assumed quiet — and `restart` additionally refuses its
// UP half, because starting daemons beside processes we cannot see would double
// whatever holds this workspace's ports. A failed STOP is not this case: it is a
// known daemon in a known state, and restart's up half still runs (documented on
// newRestartCmd).
func TestDownRecordsUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read any directory; the permission gate cannot be staged")
	}
	deny := func(t *testing.T, ws wsp.Workspace) {
		t.Helper()
		if err := os.Chmod(wsp.PidsDir(ws), 0o300); err != nil { // -wx: listable no more
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(wsp.PidsDir(ws), 0o755) }) // let TempDir clean up
	}

	t.Run("down reports it", func(t *testing.T) {
		_, ws := unlistedFixture(t, nil)
		deny(t, ws)
		err := runCLI(t, "down", "A-1")
		if err == nil {
			t.Fatal("down succeeded with an unreadable pids directory")
		}
		if !strings.Contains(err.Error(), "daemon records") {
			t.Errorf("error %q does not name the unreadable daemon records", err)
		}
		if got := xerr.ExitCode(classifyUsageError(err)); got != 1 {
			t.Errorf("exit code = %d, want 1", got)
		}
	})

	t.Run("restart refuses its up half", func(t *testing.T) {
		_, ws := unlistedFixture(t, nil)
		deny(t, ws)
		err := runCLI(t, "restart", "A-1")
		if err == nil {
			t.Fatal("restart succeeded with an unreadable pids directory")
		}
		if !strings.Contains(err.Error(), "not starting anything") {
			t.Errorf("error %q does not say the up half was refused", err)
		}
		// upWork always rewrites WORKSPACE.md, so its absence is proof the up
		// half never ran.
		if _, err := os.Stat(filepath.Join(ws.Dir, "WORKSPACE.md")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the up half ran anyway (WORKSPACE.md written): %v", err)
		}
	})
}

// TestRestartHasNoAlias pins that `restart` is its own name: `down` carries
// the `stop` alias (spec §2's synonym), but restart is not a synonym of
// anything — it is down+up, and giving it an alias would suggest otherwise.
func TestRestartHasNoAlias(t *testing.T) {
	for _, sub := range Root().Commands() {
		if sub.Name() != "restart" {
			continue
		}
		if len(sub.Aliases) != 0 {
			t.Errorf("restart has aliases %v, want none", sub.Aliases)
		}
		return
	}
	t.Fatal("no restart command registered")
}
