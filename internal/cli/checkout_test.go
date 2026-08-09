package cli

import "testing"

// TestCheckoutExitCodes pins the codes checkout.txtar can only assert as
// "non-zero" (spec §9). Unknown workspace and unconfigured project are both 3
// — the same rule status/env follow: an unresolvable half of the identifier is
// "not found", whichever half it is. Project validation happens up front,
// before any ensure work, so these cases never touch git.
func TestCheckoutExitCodes(t *testing.T) {
	notFound := map[string][]string{
		"unknown workspace":    {"checkout", "NOPE-9", "app"},
		"unconfigured project": {"checkout", "A-1", "nope"},
		// Validation covers every named project, not just the first.
		"unconfigured among configured": {"checkout", "A-1", "app", "nope"},
	}
	for name, args := range notFound {
		t.Run(name, func(t *testing.T) {
			registryRoot(t)
			if got := exitCodeFor(t, args...); got != 3 {
				t.Errorf("%v exit code = %d, want 3 (not found, spec §9)", args, got)
			}
		})
	}

	// Fewer than two positionals is a usage error (exit 2), via usageArgs.
	for name, args := range map[string][]string{
		"no args":        {"checkout"},
		"workspace only": {"checkout", "A-1"},
	} {
		t.Run(name, func(t *testing.T) {
			registryRoot(t)
			if got := exitCodeFor(t, args...); got != 2 {
				t.Errorf("%v exit code = %d, want 2 (usage error, spec §9)", args, got)
			}
		})
	}
}
