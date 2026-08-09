package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/gitx"
	"git.internal/cat/claude-workspaces-go/internal/proc"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

// newDestroyCmd builds `workspace destroy <workspace>`: run every checked-out
// project's teardown commands, then remove the worktrees, the workspace dir,
// and the allocation.
//
// Two strict phases, because teardown is the part that can fail for user
// reasons (spec §7):
//
//  1. teardown — per checked-out project in REVERSE dependency order
//     (dependents first, the mirror of setup's topo order). Command strings
//     are substituted with RuntimeVars and run via proc.Run under the curated
//     env, exactly like setup. A project's first failing command stops that
//     project's teardown; the remaining projects still run theirs. Running
//     the rest trades the strict reverse-topo invariant (a dependency's
//     teardown may now run while a failed dependent is still half up) for
//     convergence — fewer commands left to re-run, and teardown commands are
//     expected idempotent (spec §3). ANY failure aborts destroy with the
//     joined errors and NOTHING is removed, so re-running the same destroy
//     converges on whatever failed.
//  2. removal — only after every teardown succeeded: each worktree is removed
//     (force — destroy discards the working copy; the BRANCH survives, it is
//     the user's work), gated by assertInsideWorkspace so no configuration
//     can ever point removal outside the workspace dir; then the workspace
//     dir itself — gated to be strictly inside the workspaces root, because
//     a registry key is data, not something to hand os.RemoveAll untrusted;
//     then the allocation, freeing the index.
//
// An ADOPTED workspace (M4 will create these) is a dir the tool did not
// create, so the tool will not delete it: teardown + release only, worktrees
// and dir left in place, and the output says so.
//
// destroy stops no processes — daemons are M3, nothing is running yet.
func newDestroyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "destroy <workspace>",
		Short: "Tear a workspace down: teardown commands, worktrees, dir, allocation",
		// Exactly one identifier; anything else is a usage error → exit 2 (spec §9).
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Not loadRoot: destroy needs the root path itself for
			// alloc.Release, so the three steps are spelled out (registry
			// errors keep loadRoot's prefix and code).
			root, err := config.RootDir()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root) // ErrConfig → exit 4
			if err != nil {
				return err
			}
			reg, err := alloc.Load(root)
			if err != nil {
				return fmt.Errorf("registry: %w", err)
			}
			ws, err := wsp.Resolve(reg, args[0]) // ErrNotFound → exit 3
			if err != nil {
				return err
			}

			// Only what is actually checked out participates: ProjectStates is
			// git's own answer, the same one status renders.
			states := wsp.ProjectStates(cfg, ws)
			names := make([]string, len(states))
			for i, st := range states {
				names[i] = st.Name
			}
			ordered, err := wsp.TopoOrder(cfg, names)
			if err != nil {
				return err
			}
			slices.Reverse(ordered) // teardown runs dependents first (spec §7)

			out := cmd.OutOrStdout()
			var errs []error
			for _, name := range ordered {
				p := cfg.Projects[name]
				if len(p.Teardown) == 0 {
					continue
				}
				dir := wsp.ProjectDir(ws, cfg, name)
				vars := wsp.RuntimeVars(cfg, ws.Alloc.TaskID, name, ws.Alloc.Index)
				env := wsp.CommandEnv(cfg, name, ws.Alloc.TaskID, ws.Alloc.Index)
				ok := true
				for _, c := range p.Teardown {
					if err := proc.Run(dir, wsp.Subst(c, vars), env); err != nil {
						errs = append(errs, fmt.Errorf("project %q: %w", name, err))
						ok = false
						break
					}
				}
				if ok {
					fmt.Fprintf(out, "project %q: teardown complete\n", name)
				}
			}
			if len(errs) > 0 {
				return errors.Join(errs...) // nothing removed; re-run converges
			}

			if ws.Alloc.Adopted {
				if err := alloc.Release(root, ws.Dir); err != nil {
					return err
				}
				fmt.Fprintf(out, "allocation released (task %s)\n", ws.Alloc.TaskID)
				fmt.Fprintf(out, "%s left in place (adopted workspace; the tool never deletes dirs it did not create)\n", ws.Dir)
				return nil
			}

			// Removal-phase gate: ws.Dir is a raw registry key, and nothing
			// upstream constrains it to live under the workspaces root — a
			// hand-edited or corrupted .allocations.json could point it at
			// ANY directory, and the per-worktree gate below would never fire
			// for a workspace with no projects checked out. Containment before
			// ANY force-removal (controller ruling): ws.Dir must be strictly
			// inside the root. Both sides through filepath.Abs first —
			// config.RootDir may be relative (it comes from the environment;
			// new.go normalizes with Abs for exactly this reason) and
			// isAncestorOrSame treats mixed relative/absolute inputs as
			// incomparable.
			rootAbs, err := filepath.Abs(root)
			if err != nil {
				return err
			}
			dirAbs, err := filepath.Abs(ws.Dir)
			if err != nil {
				return err
			}
			if !strictlyInside(rootAbs, dirAbs) {
				return fmt.Errorf("refusing to remove %s: not strictly inside the workspaces root %s (registry entry looks corrupted)", ws.Dir, rootAbs)
			}

			for _, name := range ordered {
				dest := wsp.ProjectDir(ws, cfg, name)
				if err := assertInsideWorkspace(ws.Dir, dest); err != nil {
					return err
				}
				if err := gitx.WorktreeRemove(cfg.Projects[name].Repo, dest, true); err != nil {
					return fmt.Errorf("project %q: %w", name, err)
				}
				fmt.Fprintf(out, "project %q: worktree removed\n", name)
			}
			if err := os.RemoveAll(ws.Dir); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s removed\n", ws.Dir)
			if err := alloc.Release(root, ws.Dir); err != nil {
				return err
			}
			fmt.Fprintf(out, "allocation released (task %s)\n", ws.Alloc.TaskID)
			return nil
		},
	}
}

// assertInsideWorkspace is the containment gate in front of every
// force-removal (destroy's worktree removal above, and `new`'s undo): it
// errors unless dest is STRICTLY inside wsDir — a proper descendant,
// component-wise, not wsDir itself.
//
// Config validation already rejects a project `path` that could escape
// (absolute, `..` components), so with a config that came through Load this
// gate never fires; it is defence in depth for every other way a destination
// could be composed, and the last line before an irreversible `git worktree
// remove --force`. The component-wise question is isAncestorOrSame's
// (which.go) — the same filepath.Rel technique, shared rather than
// re-derived, so the sibling-prefix trap (/root/T-1_x "containing"
// /root/T-1_xtra) stays fixed in one place. Strictness — refusing wsDir
// itself — is what keeps a pathological dest from force-removing the
// workspace dir as if it were a worktree.
func assertInsideWorkspace(wsDir, dest string) error {
	if !strictlyInside(wsDir, dest) {
		return fmt.Errorf("refusing to remove %s: not strictly inside workspace dir %s", dest, wsDir)
	}
	return nil
}

// strictlyInside reports whether path is a PROPER descendant of dir —
// contained component-wise (isAncestorOrSame's filepath.Rel question) and not
// dir itself. This is the predicate both removal gates share: the worktree
// gate (dest inside the workspace dir) and destroy's registry gate (the
// workspace dir inside the workspaces root).
func strictlyInside(dir, path string) bool {
	return isAncestorOrSame(dir, path) && filepath.Clean(dir) != filepath.Clean(path)
}
