package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
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
