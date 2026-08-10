package cli

import (
	"testing"
)

// TestGCExitCodes pins the codes gc.txtar can only assert as "non-zero" or
// not at all (spec §9): positionals are a usage error (2), a config problem
// keeps Load's kind (4), a broken registry is a plain error (1), and a root
// with nothing to collect — including a completely empty one — is success (0).
// The batch-failure exit (any pass error → 1, after the whole batch ran) is
// exercised in gc.txtar's join-and-continue section; its code is 1 because the
// joined per-workspace errors carry no xerr kind, the same plain-error rule
// the broken-registry row pins here.
func TestGCExitCodes(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		args  []string
		want  int
	}{
		"positional arg":             {files: map[string]string{"config.yml": validConfig}, args: []string{"gc", "extra"}, want: 2},
		"missing config":             {files: nil, args: []string{"gc"}, want: 4},
		"broken registry":            {files: map[string]string{"config.yml": validConfig, ".allocations.json": "{ not json"}, args: []string{"gc"}, want: 1},
		"nothing to do":              {files: map[string]string{"config.yml": validConfig}, args: []string{"gc"}, want: 0},
		"destroy-dirs on empty root": {files: map[string]string{"config.yml": validConfig}, args: []string{"gc", "--destroy-dirs"}, want: 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fixtureRoot(t, tc.files)
			if got := exitCodeFor(t, tc.args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d (spec §9)", tc.args, got, tc.want)
			}
		})
	}
}
