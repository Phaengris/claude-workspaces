package xerr_test

import (
	"errors"
	"fmt"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

func TestExitCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"generic", errors.New("boom"), 1},
		{"usage", xerr.Wrap(xerr.ErrUsage, errors.New("bad args")), 2},
		{"not found", xerr.Wrap(xerr.ErrNotFound, errors.New("no ws")), 3},
		{"config", xerr.Wrap(xerr.ErrConfig, errors.New("bad yaml")), 4},
		{"deeply wrapped", fmt.Errorf("outer: %w", xerr.Wrap(xerr.ErrConfig, errors.New("inner"))), 4},
		{"exit carrier", xerr.Exit(7), 7},
		{"exit carrier zero", xerr.Exit(0), 0},
		{"exit carrier wrapped", fmt.Errorf("claude: %w", xerr.Exit(9)), 9},
		// A carrier whose code collides with a kind's code still wins verbatim:
		// the child's code IS the contract, indistinguishability is documented.
		{"exit carrier beats kind mapping", xerr.Wrap(xerr.ErrConfig, xerr.Exit(7)), 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := xerr.ExitCode(tc.err); got != tc.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestExitCarrier pins the carrier's shape: matchable with errors.As anywhere
// in a chain, and carrying a human-readable message for the odd path that does
// print it.
func TestExitCarrier(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", xerr.Exit(7))
	var ee *xerr.ExitError
	if !errors.As(err, &ee) {
		t.Fatal("Exit's error must be matchable via errors.As(*ExitError)")
	}
	if ee.Code != 7 {
		t.Errorf("Code = %d, want 7", ee.Code)
	}
	if want := "exit status 7"; xerr.Exit(7).Error() != want {
		t.Errorf("Error() = %q, want %q", xerr.Exit(7).Error(), want)
	}
}

func TestWrapKeepsMessage(t *testing.T) {
	err := xerr.Wrap(xerr.ErrConfig, errors.New("line 3: unknown key"))
	if !errors.Is(err, xerr.ErrConfig) {
		t.Fatal("wrapped error must satisfy errors.Is(err, ErrConfig)")
	}
	if want := "config: line 3: unknown key"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}
