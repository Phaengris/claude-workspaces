package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// exitCodeFor drives the same path as Main (Execute + usage classification) for
// the given args and reports the resulting process exit code, so usage-exit
// contracts (spec §9) can be asserted without a subprocess.
func exitCodeFor(t *testing.T, args ...string) int {
	t.Helper()
	root := Root()
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	return xerr.ExitCode(classifyUsageError(root.Execute()))
}

// TestUsageErrorsExit2 pins Finding 2: parse-layer failures exit 2, not 1.
func TestUsageErrorsExit2(t *testing.T) {
	cases := map[string][]string{
		"unknown subcommand": {"bogus-command"},
		"bad flag":           {"--bad-flag"},
		"doctor extra arg":   {"doctor", "extra-arg"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if got := exitCodeFor(t, args...); got != 2 {
				t.Errorf("exit code = %d, want 2 (usage error, spec §9)", got)
			}
		})
	}
}

func TestVersionFlag(t *testing.T) {
	root := Root()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.Contains(out.String(), "dev") {
		t.Errorf("version output %q should contain default version %q", out.String(), "dev")
	}
}
