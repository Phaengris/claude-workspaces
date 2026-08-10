package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/gitx"
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
// targeted project that is not CHECKED OUT — gitx.IsWorkTreeRoot, the same
// question every other command asks, so a missing dir and a dir that is merely
// a dir answer alike — simply has nothing running and nowhere the `stop:`
// commands belong: its epilogue is skipped, not failed (erroring would break
// restart-from-cold), but skipped LOUDLY when stop commands are actually
// configured: they may manage state outside the worktree (a `docker compose
// down`, say), and silently declining user-configured cleanup would hide that.
// One note line, still exit 0.
//
// WITH NO EXPLICIT TARGET, the inventory is the pids DIRECTORY, not the config
// (the decided "down unlisted keys" row). `down <ws>` promises the workspace is
// stopped afterwards, and a pid file is named after the key `up` wrote it under:
// rename a daemon, drop it from `start:`, or delete its project from config, and
// the record — plus the process it names, still holding this index's ports —
// becomes invisible to every config-driven walk. So the config-resolved work
// runs first (ordered, epilogues and all), and then downUnlisted gives every
// REMAINING on-disk key the same stop treatment and the same output lines. Only
// the treatment is extended: `stop:` epilogues stay config-gated exactly as
// before, because an epilogue belongs to a configured project and there is no
// project here to run one for.
//
// An EXPLICITLY named target is still resolved through the config alone (grammar
// unchanged): a name the user typed must mean what config says it means, and
// silently widening `down <ws> api` to keys config never mentions would act on
// processes nobody named.
//
// Failure policy is up's join-and-continue: resolution fails up front
// (unknown name → exit 3, nothing acted on); once stopping begins, one
// daemon's or project's failure does not abandon the rest — errors are
// collected, each prefixed `project "x": ` (or `unlisted daemon <key>: `), and
// the joined error exits 1. Nothing to do at all — no targets, nothing checked
// out, no records on disk — is a no-op SUCCESS with the shared checkout hint
// (up's convention: an empty set already complies with "stopped afterwards").
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
			targets := args[1:]
			work, err := wsp.ResolveTargets(cfg, ws, targets)
			if err != nil {
				return err // carries its own kind: 3 unknown, 2 malformed, 1 ambiguous
			}
			if len(targets) > 0 {
				// Named targets always resolve to something (ResolveTargets
				// errors otherwise), so there is no empty case to hint about.
				return downWork(cmd, cfg, ws, work)
			}
			acted, err := downAll(cmd, cfg, ws, work)
			if !acted && err == nil {
				hintNothingCheckedOut(cmd, ws)
			}
			return err
		},
	}
}

// downAll is the NO-TARGET down shared by `down` and `restart`'s down half: the
// config-resolved work first (downWork's order, epilogues included), then every
// record left in the pids directory (downUnlisted). It reports whether anything
// at all was addressed — a configured project or an on-disk key — which is what
// tells the caller whether the nothing-checked-out hint would be true.
func downAll(cmd *cobra.Command, cfg *config.Config, ws wsp.Workspace, work []wsp.TargetWork) (acted bool, err error) {
	downErr := downWork(cmd, cfg, ws, work)
	addressed, unlistedErr := downUnlisted(cmd, ws, work)
	return len(work) > 0 || addressed > 0, errors.Join(downErr, unlistedErr)
}

// downUnlisted gives the stop treatment to every daemon record in the pids
// DIRECTORY that the config-resolved work list does not already cover, in
// wsp.PidFileKeys' sorted order, and reports how many records it addressed.
//
// Those keys are exactly the ones no config-driven walk can see: a daemon
// renamed or dropped from `start:`, a project deleted from the config, a project
// whose worktree was removed by hand (so ProjectStates no longer lists it). They
// are stopped AFTER everything config-resolved, because config order encodes
// dependencies and these keys carry no order at all — putting them last means
// the known graph is unwound first, and what remains is unwound in a stable
// (alphabetical) sequence rather than an arbitrary one.
//
// An unreadable pids directory is an ERROR, never an assumed quiet: "I cannot
// tell what runs here" must not be reported as "nothing runs here" by a command
// whose whole promise is that nothing does.
func downUnlisted(cmd *cobra.Command, ws wsp.Workspace, work []wsp.TargetWork) (addressed int, err error) {
	keys, err := wsp.PidFileKeys(ws)
	if err != nil {
		return 0, fmt.Errorf("reading daemon records: %w", err)
	}
	covered := map[string]bool{}
	for _, w := range work {
		for _, d := range w.Daemons {
			covered[d.Key()] = true
		}
	}
	out := cmd.OutOrStdout()
	var errs []error
	for _, key := range keys {
		if covered[key] {
			continue // downWork already handled it; one line per daemon, ever
		}
		addressed++
		if err := stopRecord(out, filepath.Join(wsp.PidsDir(ws), key), key); err != nil {
			errs = append(errs, fmt.Errorf("unlisted daemon %s: %w", key, err))
		}
	}
	return addressed, errors.Join(errs...)
}

// stopRecord applies the stop treatment to ONE daemon record — the shared body
// of every stop in the tool, addressed by pid-file PATH and display KEY rather
// than by a wsp.Daemon, because the no-target down must also stop keys no
// config-derived Daemon value exists for (see downUnlisted).
//
// It prints the line the user reads (`already stopped` / `stopped <key> (SIG)`)
// and returns only what went WRONG, unprefixed: the caller knows whether to say
// `project "x": <key>: …` or `unlisted daemon <key>: …`.
//
// The record is read ONCE, and that single read answers the liveness question
// wsp.DaemonState asks (ReadPidFile + proc.Alive) while also yielding the
// (pid, starttime) pair the stop itself needs. Asking DaemonState first would
// read the file a second time and open a window between the two reads — the
// race the previous shape had to carry an extra branch for. Every unusable
// record — missing, corrupt, naming a dead or recycled pid — is `already
// stopped` and is LEFT on disk: nothing here can name a process anyone could
// stop (the decided Liveness row), and reaping such records is gc's job.
func stopRecord(out io.Writer, pidPath, key string) error {
	pid, starttime, err := proc.ReadPidFile(pidPath)
	if err != nil || !proc.Alive(pid, starttime) {
		fmt.Fprintf(out, "%s already stopped\n", key)
		return nil
	}
	signal, err := proc.StopGroup(pid, starttime)
	if err != nil {
		return err // failed stop: leave the pid record for a retry
	}
	// Confirmed not alive (freshly killed, or the "" no-op: it vanished
	// between the liveness check and the signal) — either way the record is now
	// stale, and removing it is THIS caller's contractual job. Already-gone is
	// fine: someone else finishing the removal is still the promised end state
	// (convergence over a spurious exit 1).
	if err := os.Remove(pidPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing pid file: %w", err)
	}
	if signal == "" {
		fmt.Fprintf(out, "%s already stopped\n", key)
		return nil
	}
	fmt.Fprintf(out, "stopped %s (%s)\n", key, signal)
	return nil
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
// then — when the whole project was targeted and git reports its dir checked
// out — the `stop:` epilogue. The returned error is project-prefixed and may
// join several daemons' failures; one daemon's failed stop never blocks its
// siblings (they are independent processes), and the epilogue still runs —
// `stop:` commands exist to clean up, which matters most when something
// already went wrong.
func downProject(cmd *cobra.Command, cfg *config.Config, ws wsp.Workspace, w wsp.TargetWork) error {
	fail := func(err error) error { return fmt.Errorf("project %q: %w", w.Project, err) }

	var errs []error
	for i := len(w.Daemons) - 1; i >= 0; i-- {
		d := w.Daemons[i]
		if err := stopRecord(cmd.OutOrStdout(), wsp.PidPath(ws, d), d.Key()); err != nil {
			errs = append(errs, fail(fmt.Errorf("%s: %w", d.Key(), err)))
		}
	}

	if w.WholeProject {
		stops := wsp.StopCommands(cfg, w.Project)
		dir := wsp.ProjectDir(ws, cfg, w.Project)
		if !gitx.IsWorkTreeRoot(dir) {
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
