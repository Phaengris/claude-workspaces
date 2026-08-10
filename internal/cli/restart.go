package cli

import (
	"errors"
	"fmt"

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
// THE HALVES ARE NOT SYMMETRIC with no explicit target, and cannot be: the down
// half is the real no-target down, so it stops every live key recorded in the
// pids DIRECTORY, config-known or not (downAll — the decided "down unlisted
// keys" row); the up half can only start what the config DEFINES, since a
// daemon's command lives in `start:` and nothing else records it. So restarting a
// workspace whose config dropped a daemon STOPS that daemon and does not bring
// it back — the honest reading of "converge to running": what config no longer
// defines has no running state to converge to. Restore the config entry (or use
// `up`) to start it again.
//
// The up half is REFUSED outright in one case: the pids directory could not be
// listed (errRecordsUnreadable). Starting daemons beside processes we cannot see
// would double whatever holds this workspace's ports, and "cannot tell" is a
// refusal everywhere else in this tool too — see the branch itself.
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
			targets := args[1:]
			work, err := wsp.ResolveTargets(cfg, ws, targets)
			if err != nil {
				return err // carries its own kind: 3 unknown, 2 malformed, 1 ambiguous
			}
			// Locals in both branches make the sequencing unmistakable: the
			// down half runs to completion before the up half begins, whatever
			// it returned.
			if len(targets) > 0 {
				// Named targets always resolve to something, and the down half
				// is the config-resolved one — the grammar is unchanged.
				downErr := downWork(cmd, cfg, ws, work)
				upErr := upWork(cmd, cfg, ws, work)
				return errors.Join(downErr, upErr)
			}
			acted, downErr := downAll(cmd, cfg, ws, work)
			if !acted && downErr == nil {
				// Nothing checked out AND nothing running on disk: there is
				// neither anything to stop nor anything to start.
				hintNothingCheckedOut(cmd, ws)
				return nil
			}
			if errors.Is(downErr, errRecordsUnreadable) {
				// The daemon INVENTORY could not be read, so what is running
				// here is unknown — and the up half would start daemons next to
				// it, doubling processes that hold this workspace's ports.
				// Refuse the up half entirely: "cannot tell" is a stop, not a
				// green light (the same doctrine gc's destroy gate and release
				// apply). A failed STOP is different and keeps the documented
				// contract: it is a known daemon in a known state, the up half
				// reads it as `already running`, and the error still exits 1.
				return fmt.Errorf("%w; not starting anything while it is unknown what runs here", downErr)
			}
			upErr := upWork(cmd, cfg, ws, work)
			return errors.Join(downErr, upErr)
		},
	}
}
