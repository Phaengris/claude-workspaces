package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/proc"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

// newDownCmd builds `workspace down <workspace> [target…]` (alias `stop`):
// converge the targeted daemons to stopped — the mirror of `up`, walked
// BACKWARDS (the decided Order row): projects in reverse topological order,
// and within each project its daemons in reverse listed order, so dependents
// go down before what they depend on. Per daemon:
//
//   - not running (per pid+starttime liveness) → `already stopped`, nothing
//     signaled — idempotence, the decided rule;
//   - running → proc.StopGroup (TERM to the process group, escalate to KILL),
//     then remove the pid file — the caller-removes contract: StopGroup never
//     touches it, so a FAILED stop leaves the record intact for retry — and
//     report which signal sufficed: `stopped app:rails (TERM)` / `(KILL)`.
//     That line promises the recorded LEADER is confirmed dead, not every
//     group member — a member that ignores TERM can outlive a leader that
//     exits on it (StopGroup's documented limitation).
//
// After a project's daemons, when the WHOLE project was targeted, its `stop:`
// config commands run via proc.Run (same rendering as everywhere: command
// string through Subst, env from CommandEnv) — they are the project's
// epilogue, so a single-daemon target must not trigger them, exactly as it
// does not trigger `up`'s run-and-waits.
//
// down never runs EnsureProject: stopping must not create worktrees. A
// targeted project that was never checked out simply has nothing running and
// no dir to run `stop:` commands in — its epilogue is skipped, not failed
// (erroring would break restart-from-cold), but skipped LOUDLY when stop
// commands are actually configured: they may manage state outside the
// worktree (a `docker compose down`, say), and silently declining
// user-configured cleanup would hide that. One note line, still exit 0.
//
// Failure policy is up's join-and-continue: resolution fails up front
// (unknown name → exit 3, nothing acted on); once stopping begins, one
// daemon's or project's failure does not abandon the rest — errors are
// collected, each prefixed `project "x": `, and the joined error exits 1.
// An empty work list (no targets, nothing checked out) is a no-op SUCCESS
// with the shared checkout hint (up's convention: `down` promises "stopped
// afterwards", and an empty set already complies).
func newDownCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "down <workspace> [target...]",
		Aliases: []string{"stop"},
		Short:   "Stop daemons: confirmed group stop, then stop commands",
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
			return downWork(cmd, cfg, ws, work)
		},
	}
}

// downWork stops an already-resolved work list: downProject per entry in
// REVERSE of the given (topological) order, join-and-continue. Shared by
// `down` and `restart`'s down half.
func downWork(cmd *cobra.Command, cfg *config.Config, ws wsp.Workspace, work []wsp.TargetWork) error {
	var errs []error
	for i := len(work) - 1; i >= 0; i-- {
		if err := downProject(cmd, cfg, ws, work[i]); err != nil {
			errs = append(errs, err) // already prefixed `project "<name>": …`
		}
	}
	return errors.Join(errs...)
}

// downProject stops one resolved project: daemons in reverse listed order,
// then — when the whole project was targeted and its dir exists — the `stop:`
// epilogue. The returned error is project-prefixed and may join several
// daemons' failures; one daemon's failed stop never blocks its siblings (they
// are independent processes), and the epilogue still runs — `stop:` commands
// exist to clean up, which matters most when something already went wrong.
func downProject(cmd *cobra.Command, cfg *config.Config, ws wsp.Workspace, w wsp.TargetWork) error {
	fail := func(err error) error { return fmt.Errorf("project %q: %w", w.Project, err) }

	var errs []error
	for i := len(w.Daemons) - 1; i >= 0; i-- {
		d := w.Daemons[i]
		if running, _ := wsp.DaemonState(ws, d); !running {
			fmt.Fprintf(cmd.OutOrStdout(), "%s already stopped\n", d.Key())
			continue
		}
		pidPath := wsp.PidPath(ws, d)
		pid, starttime, err := proc.ReadPidFile(pidPath)
		if err != nil {
			// DaemonState just read this file successfully, so reaching here
			// takes a race. The file VANISHING is the daemon converging to
			// stopped on its own (or a concurrent down) — the state down
			// promises, so success, not error. Corruption is still reported:
			// with no (pid, starttime) there is nothing safe to signal.
			if errors.Is(err, fs.ErrNotExist) {
				fmt.Fprintf(cmd.OutOrStdout(), "%s already stopped\n", d.Key())
				continue
			}
			errs = append(errs, fail(fmt.Errorf("%s: %w", d.Key(), err)))
			continue
		}
		signal, err := proc.StopGroup(pid, starttime)
		if err != nil {
			errs = append(errs, fail(fmt.Errorf("%s: %w", d.Key(), err)))
			continue // failed stop: leave the pid record for a retry
		}
		// Confirmed not alive (freshly killed, or the "" no-op: it vanished
		// between the liveness check and the signal) — either way the record
		// is now stale, and removing it is THIS caller's contractual job.
		// Already-gone is fine: someone else finishing the removal is still
		// the promised end state (convergence over a spurious exit 1).
		if err := os.Remove(pidPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fail(fmt.Errorf("%s: removing pid file: %w", d.Key(), err)))
			continue
		}
		if signal == "" {
			fmt.Fprintf(cmd.OutOrStdout(), "%s already stopped\n", d.Key())
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "stopped %s (%s)\n", d.Key(), signal)
	}

	if w.WholeProject {
		stops := wsp.StopCommands(cfg, w.Project)
		dir := wsp.ProjectDir(ws, cfg, w.Project)
		if _, statErr := os.Stat(dir); statErr != nil {
			// Not checked out: nothing to run the epilogue in, and down must
			// not create worktrees. Note the skip when there WAS something to
			// skip; a project without stop commands loses nothing, so silence.
			if len(stops) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: stop commands skipped (not checked out)\n", w.Project)
			}
		} else {
			vars := wsp.RuntimeVars(cfg, ws.Alloc.TaskID, w.Project, ws.Alloc.Index)
			env := wsp.CommandEnv(cfg, w.Project, ws.Alloc.TaskID, ws.Alloc.Index)
			for _, c := range stops {
				if err := proc.Run(dir, wsp.Subst(c, vars), env); err != nil {
					errs = append(errs, fail(err)) // reads "command failed: <reason>"
				}
			}
		}
	}
	return errors.Join(errs...)
}
