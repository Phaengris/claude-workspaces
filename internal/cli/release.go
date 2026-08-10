package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

// alreadyReleased is the no-arg no-op-success line: the cwd is not inside any
// tracked workspace, which is exactly the state release exists to reach — so it
// is success, and "already released" is the whole story (the ancestry walk found
// nothing to release anywhere above here).
//
// The EXPLICIT-dir miss says something else, `no allocation for <dir>`, because
// an explicit path is matched exactly: pointing release at a SUBDIRECTORY of a
// live workspace misses, and answering "already released" there would assert
// something false about a workspace that is still very much tracked. Both are
// exit 0 — release is idempotent (the decided row) and a miss is not a failure —
// they just differ in what they claim.
const alreadyReleased = "already released"

// newReleaseCmd builds `workspace release [dir]`: drop a workspace's
// allocation and leave every byte on disk exactly where it is.
//
// It is the inverse of `adopt` and the opposite of `destroy`: destroy tears the
// stack down and removes what the tool created, release only stops tracking.
// NOTHING here MODIFIES the filesystem beyond the registry — no worktree
// removal, no teardown commands, not even for a tool-created workspace. That
// is deliberate: release is the escape hatch for "this is mine now, stop
// managing it", and a command that sometimes deleted things would be unusable
// for that. It does READ the workspace's pid files (see the refusal below);
// "never touches disk" has always meant never writes or deletes, and a refusal
// that has to know whether processes are alive cannot avoid reading their
// records.
//
// The ONE refusal: a workspace with a live daemon is not releasable. The
// allocation is what makes the workspace addressable and its index exclusive,
// so dropping it while a process still holds that index's ports would strand
// the daemon twice over — `down <name>` could no longer resolve the workspace
// to stop it, and the freed index would be handed to the next `new`, whose
// daemons would then collide on the same ports. This is M3's destroy-orphan bug
// in registry form, and the answer is the same: refuse (plain error, exit 1)
// and name the command that fixes it, `workspace down <name>`. The liveness
// question is anyDaemonRunning — gc's own gate, reading the pids DIRECTORY, so
// a daemon renamed out of the config while running still blocks the release.
//
// Which workspace:
//   - `release <dir>` releases THAT directory's allocation (made absolute), by
//     exact match — allocationAt, not the ancestry walk. An explicit path is a
//     precise instruction and must not silently climb to a parent workspace.
//   - `release` with no argument releases the workspace CONTAINING the current
//     directory — the same `which` ancestry walk, so it works from any
//     subdirectory, which is where a user actually is when they decide to stop
//     tracking the thing they are standing in.
//
// A missing allocation is success, exit 0: release is idempotent (the decided
// row), and that covers both re-runs and the no-arg case run from outside any
// workspace. The LINE differs by path — see alreadyReleased — because only the
// no-arg walk can honestly say "you are not in a tracked workspace". There is
// deliberately no ErrNotFound path — `which` is the command that answers "am I
// in a workspace?", and release refusing to be re-runnable would make it
// useless in cleanup scripts.
func newReleaseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "release [dir]",
		Short: "Release a workspace's allocation, leaving its files untouched",
		// At most one positional; more is a usage error → exit 2 (spec §9).
		Args: usageArgs(cobra.MaximumNArgs(1)),
		// --json is inherited and deliberately unused: spec §2 scopes it to the
		// query commands. Accepting and ignoring it keeps `workspace --json
		// release` working for a caller that sets the flag globally.
		RunE: func(cmd *cobra.Command, args []string) error {
			// Not loadRoot: release needs the root path itself for
			// alloc.Release. The config is loaded all the same (loadRootDir),
			// so a broken root fails the same way here as everywhere else —
			// exit 4 for config, 1 for an unreadable registry.
			root, _, reg, err := loadRootDir()
			if err != nil {
				return err
			}
			ws, ok, miss, err := releaseTarget(reg, args)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !ok {
				fmt.Fprintln(out, miss)
				return nil
			}
			// The one refusal: an index whose ports live processes still hold
			// must not be freed (see the doc comment).
			running, err := anyDaemonRunning(ws)
			if err != nil {
				return err
			}
			if running {
				return fmt.Errorf("%s still has running daemons; stop them first: workspace down %s",
					ws.Name(), ws.Name())
			}
			// ws.Dir is the registry's own key spelling (allocationAt and
			// workspaceAt both return it), so the delete always finds it.
			if err := alloc.Release(root, ws.Dir); err != nil {
				return err
			}
			fmt.Fprintf(out, "released %s (#%d freed)\n", ws.Name(), ws.Alloc.Index)
			return nil
		},
	}
}

// releaseTarget resolves which allocation a `release` invocation names: the
// exact directory when one is given, otherwise the workspace containing the
// cwd. The bool is "there is an allocation to drop" — false is the idempotent
// no-op, not a failure — and the string is the line to print in that case,
// which differs by path (see alreadyReleased): the explicit-dir miss names the
// directory it matched against, the cwd miss says the cwd is untracked. The
// error is reserved for a genuinely broken environment (an unreadable cwd, an
// unresolvable path).
func releaseTarget(reg alloc.Registry, args []string) (ws wsp.Workspace, ok bool, miss string, err error) {
	if len(args) == 1 {
		dir, err := filepath.Abs(args[0])
		if err != nil {
			return wsp.Workspace{}, false, "", err
		}
		ws, ok := allocationAt(reg, dir)
		return ws, ok, "no allocation for " + dir, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return wsp.Workspace{}, false, "", err
	}
	ws, ok = workspaceAt(reg, cwd)
	return ws, ok, alreadyReleased, nil
}
