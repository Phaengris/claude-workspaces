package cli

import (
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
