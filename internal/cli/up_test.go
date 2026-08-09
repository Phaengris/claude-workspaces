package cli

import (
	"testing"
)

// TestUpExitCodes pins the codes up.txtar can only assert as "non-zero" (spec
// §9). Both halves of an unresolvable identifier are 3 — an unknown workspace
// and an unknown target follow the same not-found rule checkout/env/cd pin —
// and a missing workspace argument is a usage error, 2. The registryRoot
// fixture's workspace dir does not exist on disk, so `up A-1` with no targets
// resolves to an empty work list: converge-on-nothing is a no-op SUCCESS
// (spec's ensure doctrine — `up` promises "running afterwards", and an empty
// set already is), printing the checkout hint rather than failing.
func TestUpExitCodes(t *testing.T) {
	cases := map[string]struct {
		args []string
		want int
	}{
		"unknown workspace":   {args: []string{"up", "NOPE-9"}, want: 3},
		"unknown target":      {args: []string{"up", "A-1", "nope"}, want: 3},
		"no args":             {args: []string{"up"}, want: 2},
		"nothing checked out": {args: []string{"up", "A-1"}, want: 0},
		// The alias must classify identically to the canonical name.
		"alias start, unknown target": {args: []string{"start", "A-1", "nope"}, want: 3},
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
