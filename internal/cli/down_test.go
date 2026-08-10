package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
func unlistedFixture(t *testing.T, records map[string]string) string {
	t.Helper()
	const cfg = `values:
  PORT: { start: 5000, per_workspace: 10 }
projects:
  api:
    repo: /tmp/api-src
    start:
      - one: sleep 30
`
	root := fixtureRoot(t, map[string]string{"config.yml": cfg})
	wsDir := filepath.Join(root, "A-1_x")
	pids := filepath.Join(wsDir, ".workspace", "pids")
	if err := os.MkdirAll(pids, 0o755); err != nil {
		t.Fatal(err)
	}
	reg := `{"` + wsDir + `": {"index": 0, "task_id": "A-1", ` +
		`"description": "d", "created_at": "2026-08-01T09:00:00Z", "adopted": false}}`
	if err := os.WriteFile(filepath.Join(root, ".allocations.json"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
	for key, content := range records {
		if err := os.WriteFile(filepath.Join(pids, key), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
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
// Records here are DEAD (a mismatched starttime) and CORRUPT, so they exercise
// the enumeration and the dedupe without any process: the live case needs a real
// daemon and lives in down_restart.txtar (signaling a hand-written record risks
// aiming at this very test process).
func TestDownEnumeratesPidsDir(t *testing.T) {
	// pid 1 exists (kill(1,0) is EPERM at worst) but its starttime cannot be
	// this, so the record reads dead — no signal is ever sent.
	const dead = "1 99999999999\n"

	t.Run("unlisted keys get the stop treatment", func(t *testing.T) {
		unlistedFixture(t, map[string]string{"gone:ghost": dead, "gone:corrupt": "garbage\n"})
		got := downLines(t, "down", "A-1")
		want := []string{"gone:corrupt already stopped", "gone:ghost already stopped"}
		if !slices.Equal(got, want) {
			t.Errorf("down A-1 printed %q, want %q (pids-dir enumeration, ReadDir order)", got, want)
		}
	})

	t.Run("a config-known key is treated once, not twice", func(t *testing.T) {
		unlistedFixture(t, map[string]string{"api:one": dead})
		// api is configured but NOT checked out, so the config-driven work list
		// is empty and the record is reached by the enumeration alone — one
		// line either way, never one per pass.
		got := downLines(t, "down", "A-1")
		want := []string{"api:one already stopped"}
		if !slices.Equal(got, want) {
			t.Errorf("down A-1 printed %q, want %q (no double treatment)", got, want)
		}
	})

	t.Run("an EXPLICIT target stays config-resolved", func(t *testing.T) {
		unlistedFixture(t, map[string]string{"gone:ghost": dead})
		// The grammar is unchanged: naming `api` acts on api's daemons and
		// nothing else, however much else the directory records.
		got := downLines(t, "down", "A-1", "api")
		want := []string{"api:one already stopped"}
		if !slices.Equal(got, want) {
			t.Errorf("down A-1 api printed %q, want %q (explicit targets never enumerate)", got, want)
		}
	})

	t.Run("records addressed means no nothing-checked-out hint", func(t *testing.T) {
		unlistedFixture(t, map[string]string{"gone:ghost": dead})
		for _, line := range downLines(t, "down", "A-1") {
			if strings.Contains(line, "nothing checked out") {
				t.Errorf("hint printed alongside addressed records: %q", line)
			}
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
		unlistedFixture(t, map[string]string{"gone:ghost": dead})
		got := downLines(t, "restart", "A-1")
		if !slices.Contains(got, "gone:ghost already stopped") {
			t.Errorf("restart A-1 printed %q, want the unlisted record addressed", got)
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
