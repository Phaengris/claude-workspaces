package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

// alreadyReleased is release's no-op-success line. One string for every way of
// getting there — released a moment ago, never allocated, not a workspace at
// all — because from the caller's side they are the same fact: this directory
// is not tracked, which is what was asked for.
const alreadyReleased = "already released"

// newReleaseCmd builds `workspace release [dir]`: drop a workspace's
// allocation and leave every byte on disk exactly where it is.
//
// It is the inverse of `adopt` and the opposite of `destroy`: destroy tears the
// stack down and removes what the tool created, release only stops tracking.
// NOTHING here touches the filesystem beyond the registry — no worktree
// removal, no teardown commands, not even for a tool-created workspace. That
// is deliberate: release is the escape hatch for "this is mine now, stop
// managing it", and a command that sometimes deleted things would be unusable
// for that.
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
// A missing allocation is success, exit 0, printing alreadyReleased: release is
// idempotent (the decided row), and that covers both re-runs and the no-arg
// case run from outside any workspace. There is deliberately no ErrNotFound
// path — `which` is the command that answers "am I in a workspace?", and
// release refusing to be re-runnable would make it useless in cleanup scripts.
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
			ws, ok, err := releaseTarget(reg, args)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !ok {
				fmt.Fprintln(out, alreadyReleased)
				return nil
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
// no-op, not a failure. The error is reserved for a genuinely broken
// environment (an unreadable cwd, an unresolvable path).
func releaseTarget(reg alloc.Registry, args []string) (wsp.Workspace, bool, error) {
	if len(args) == 1 {
		dir, err := filepath.Abs(args[0])
		if err != nil {
			return wsp.Workspace{}, false, err
		}
		ws, ok := allocationAt(reg, dir)
		return ws, ok, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return wsp.Workspace{}, false, err
	}
	ws, ok := workspaceAt(reg, cwd)
	return ws, ok, nil
}
