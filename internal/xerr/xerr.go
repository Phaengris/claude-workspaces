// Package xerr defines the tool's error kinds. main maps them to exit codes,
// so scripts and the Claude skill can distinguish usage errors from missing
// workspaces from config problems (spec §9).
package xerr

import (
	"errors"
	"fmt"
)

// ErrUsage, ErrNotFound, and ErrConfig are the error kinds callers wrap with
// Wrap. ExitCode maps them to distinct process exit codes; match them with
// errors.Is, not equality, because they are always returned wrapped.
var (
	ErrUsage    = errors.New("usage")
	ErrNotFound = errors.New("not found")
	ErrConfig   = errors.New("config")
)

// Wrap attaches a kind to err. Both remain matchable via errors.Is.
func Wrap(kind, err error) error {
	return fmt.Errorf("%w: %w", kind, err)
}

// ExitCode maps an error chain to the process exit code (spec §9). It reports 0
// for nil, 2/3/4 for the ErrUsage/ErrNotFound/ErrConfig kinds anywhere in the
// chain, and 1 for any other non-nil error.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrUsage):
		return 2
	case errors.Is(err, ErrNotFound):
		return 3
	case errors.Is(err, ErrConfig):
		return 4
	default:
		return 1
	}
}
