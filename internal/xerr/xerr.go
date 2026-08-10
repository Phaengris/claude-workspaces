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

// ExitError carries a LITERAL process exit code up to Main — the vehicle for
// "the child's code becomes ours" (the `claude` session runner). It is not a
// kind: ExitCode honors it before any kind mapping, verbatim, so a child
// exiting 3 is indistinguishable from ErrNotFound — a documented, accepted
// collision (v1 behaved the same; the child owns its own code space).
type ExitError struct{ Code int }

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

// Exit returns an error that makes the process exit with code, verbatim.
// Match it with errors.As and a *ExitError target — cli.Main does, to suppress its
// "workspace:" stderr line for these (the child already owned the terminal;
// the code itself is the message).
func Exit(code int) error {
	return &ExitError{Code: code}
}

// ExitCode maps an error chain to the process exit code (spec §9). It reports
// 0 for nil; an ExitError's code verbatim (checked FIRST — a propagated child
// code outranks any kind also in the chain); 2/3/4 for the ErrUsage/
// ErrNotFound/ErrConfig kinds; and 1 for any other non-nil error.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *ExitError
	switch {
	case errors.As(err, &ee):
		return ee.Code
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
