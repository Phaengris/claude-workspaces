package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

// newRestartCmd builds `workspace restart <workspace> [target…]`: the down
// flow (confirmed deaths) then the up flow, over the SAME resolved target set
// — restart ≡ down+up, nothing more. That composition is also its semantics:
// restart CONVERGES to running (the decided restart row), so a targeted
// daemon that was already stopped reads `already stopped` from the down half
// and starts in the up half — restart of a cold set is just up. No targets
// means the whole workspace, like both halves.
//
// The up half is the REAL up, EnsureProject included — restarting a project
// re-runs its run-and-waits and, from cold, checks it out. The down half is
// the real down: reverse order, `stop:` epilogue when the whole project was
// targeted, pid files removed only on confirmed death.
//
// Both halves join-and-continue and their errors are joined across the seam:
// a daemon whose stop FAILED is still alive, so the up half reads it as
// `already running` and leaves it alone — the failure surfaces once, from the
// down half, and exits 1.
//
// No alias: `down` has `stop` as its synonym, but restart is not a synonym of
// anything.
func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <workspace> [target...]",
		Short: "Restart daemons: confirmed stop, then converge to running",
		// At least the workspace; fewer is a usage error → exit 2 (spec §9).
		Args: usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, reg, err := loadRoot()
			if err != nil {
				return err
			}
			ws, err := wsp.Resolve(reg, args[0]) // ErrNotFound → exit 3
			if err != nil {
				return err
			}
			work, err := wsp.ResolveTargets(cfg, ws, args[1:])
			if err != nil {
				return err // carries its own kind: 3 unknown, 2 malformed, 1 ambiguous
			}
			if len(work) == 0 {
				hintNothingCheckedOut(cmd, ws)
				return nil
			}
			// Locals make the sequencing unmistakable: the down half runs to
			// completion before the up half begins, whatever it returned.
			downErr := downWork(cmd, cfg, ws, work)
			upErr := upWork(cmd, cfg, ws, work)
			return errors.Join(downErr, upErr)
		},
	}
}
