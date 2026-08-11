package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Phaengris/claude-workspaces/internal/config"
	"github.com/Phaengris/claude-workspaces/internal/proc"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
)

// newUpCmd builds `workspace up <workspace> [target…]` (alias `start`):
// converge the targeted daemons to running. Per resolved project, in
// dependency order (the decided Order row):
//
//  1. wsp.EnsureProject — the FULL checkout chain (worktree, .env, stamped
//     setup), unconditionally: a single-daemon target still needs its owning
//     project checked out (the decided "up setup" row), and each step is
//     idempotent so an already-ensured project costs a few stats;
//  2. run-and-waits (bare `start:` entries) via proc.Run, in listed order,
//     only when the WHOLE project was targeted — they are the project's
//     prelude, not any single daemon's. The first failure aborts THIS
//     project (a project whose prelude failed must not run half its stack:
//     its daemons do not start) but not the others;
//  3. daemons in listed order: already running → note + skip, leaving the
//     pid record untouched (idempotence, the decided rule); otherwise start
//     via proc.StartDaemon and report the new pid. `started` means SPAWNED
//     and recorded, not healthy — a daemon that exits immediately surfaces
//     in its .err.log and reads as not running from then on.
//
// Commands run with the command STRING substituted (RuntimeVars) and the
// process env from CommandEnv — the same rendering as setup commands.
//
// Failure policy is checkout's join-and-continue: target resolution fails up
// front (unknown name → exit 3, nothing acted on), but once starting begins
// one project's failure does not abandon the rest — errors are collected,
// each prefixed `project "x": ` (EnsureProject prefixes its own), and the
// joined error exits 1.
//
// An empty work list (no targets, nothing checked out) is a no-op SUCCESS:
// `up` promises "running afterwards", and an empty set already complies —
// the ensure doctrine, decided over the plain-error alternative. It prints a
// hint pointing at checkout, since a user typing `up` almost certainly wants
// something running.
func newUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "up <workspace> [target...]",
		Aliases: []string{"start"},
		Short:   "Start projects: ensure-chain, run-and-waits, daemons",
		// At least the workspace; fewer is a usage error → exit 2 (spec §9).
		Args: usageArgs(cobra.MinimumNArgs(1)),
		// --json is inherited and deliberately unused: spec §2 scopes it to
		// the query commands (same stance as checkout/new).
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
			return upWork(cmd, cfg, ws, work)
		},
	}
}

// hintNothingCheckedOut prints the shared no-op-success hint of up, down and
// restart: an empty resolved work list means the workspace has nothing checked
// out, converge-on-nothing already complies with every one of those commands'
// promises (the ensure doctrine), and a user typing any of them almost
// certainly wants something checked out first.
func hintNothingCheckedOut(cmd *cobra.Command, ws wsp.Workspace) {
	fmt.Fprintf(cmd.OutOrStdout(),
		"nothing checked out — run: workspace checkout %s <project…>\n", ws.Name())
}

// upWork converges an already-resolved work list to running: upProject per
// entry in the given (topological) order, join-and-continue — one project's
// failure does not abandon the rest. Shared by `up` and `restart`'s up half.
//
// up checks projects out (EnsureProject creates worktrees), so WORKSPACE.md
// is refreshed like checkout/new do — ensure.go makes that the caller's job.
// Refreshed REGARDLESS of failures: a half-succeeded up has changed what is
// checked out, and the file must describe reality, not the happy path.
func upWork(cmd *cobra.Command, cfg *config.Config, ws wsp.Workspace, work []wsp.TargetWork) error {
	var errs []error
	for _, w := range work {
		if err := upProject(cmd, cfg, ws, w); err != nil {
			errs = append(errs, err) // already prefixed `project "<name>": …`
		}
	}
	if err := wsp.WriteWorkspaceMD(cfg, ws); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...) // nil when everything succeeded
}

// upProject converges one resolved project: ensure-chain, prelude, daemons.
// The returned error is project-prefixed and may join several daemons'
// failures; ensure/prelude failures abort the project, a single daemon's
// start failure only skips that daemon (the remaining daemons are
// independent processes — one bad command should not hold its siblings
// hostage the way a failed prelude must).
func upProject(cmd *cobra.Command, cfg *config.Config, ws wsp.Workspace, w wsp.TargetWork) error {
	fail := func(err error) error { return fmt.Errorf("project %q: %w", w.Project, err) }

	if err := wsp.EnsureProject(cfg, ws, w.Project); err != nil {
		return err // EnsureProject prefixes its own errors
	}

	dir := wsp.ProjectDir(ws, cfg, w.Project)
	vars := wsp.RuntimeVars(cfg, ws.Alloc.TaskID, w.Project, ws.Alloc.Index)
	env := wsp.CommandEnv(cfg, w.Project, ws.Alloc.TaskID, ws.Alloc.Index)

	if w.WholeProject {
		for _, c := range wsp.RunAndWaits(cfg, w.Project) {
			if err := proc.Run(dir, wsp.Subst(c, vars), env); err != nil {
				return fail(err) // proc.Run's message reads "command failed: <reason>"
			}
		}
	}

	var errs []error
	for _, d := range w.Daemons {
		if running, pid := wsp.DaemonState(ws, d); running {
			fmt.Fprintf(cmd.OutOrStdout(), "%s already running (pid %d)\n", d.Key(), pid)
			continue
		}
		pidPath := wsp.PidPath(ws, d)
		logPath := wsp.LogPath(ws, d)
		// PidPath/LogPath do not create parents (their contract); the pids
		// and logs dirs are made here, just before the first file lands in
		// them. MkdirAll is idempotent, so repeats cost nothing.
		if err := errors.Join(
			os.MkdirAll(filepath.Dir(pidPath), 0o755),
			os.MkdirAll(filepath.Dir(logPath), 0o755),
		); err != nil {
			// No dirs means no daemon can start: abort the project — but
			// keep whatever daemon failures were already collected.
			return errors.Join(append(errs, fail(err))...)
		}
		if err := proc.StartDaemon(dir, wsp.Subst(d.Cmd, vars), env,
			logPath, wsp.ErrLogPath(ws, d), pidPath); err != nil {
			errs = append(errs, fail(fmt.Errorf("%s: %w", d.Key(), err)))
			continue
		}
		// StartDaemon reports success, not the pid — read it back from the
		// record it just wrote, which is also the record `down` will act on.
		pid, _, err := proc.ReadPidFile(pidPath)
		if err != nil {
			errs = append(errs, fail(fmt.Errorf("%s: started but %w", d.Key(), err)))
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "started %s (pid %d)\n", d.Key(), pid)
	}
	return errors.Join(errs...)
}
