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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := xerr.ExitCode(tc.err); got != tc.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
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
