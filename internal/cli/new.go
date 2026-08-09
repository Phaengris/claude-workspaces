package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/gitx"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// undoStack collects compensating actions for `new`, the one transactional
// command. Actions are pushed as the transaction progresses and run() executes
// them LIFO — later steps depend on earlier ones (a worktree lives inside the
// workspace dir, the dir belongs to the allocation), so tearing down in
// reverse is what keeps each undo operating on state its own step still
// understands. Undo failures never mask the original error: run() collects
// every failure and the caller joins them onto it.
type undoStack []func() error

func (u *undoStack) push(fn func() error) { *u = append(*u, fn) }

func (u undoStack) run() error {
	var errs []error
	for i := len(u) - 1; i >= 0; i-- {
		if err := u[i](); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// newNewCmd builds `workspace new <task_id> <desc> [project…]`: allocate a
// workspace, scaffold its docs, and check the named projects out — as a
// transaction. checkout converges by re-running; new instead promises that a
// failed invocation leaves NOTHING behind (spec: `new` on an existing
// workspace is a conflict, so a half-created one would wedge the name).
//
// Check order is a contract:
//  1. task-id validation — usage error, exit 2;
//  2. every named project configured — exit 3, before any mutation (same
//     rule and message as checkout/env/cd);
//  3. conflict pre-checks, exit 1 plain errors naming the conflict: the
//     workspace dir must not exist on disk, and alloc.Allocate rejects a
//     duplicate dir or task id (its error already names the existing dir).
//
// Then the transaction: allocate → mkdir → CLAUDE.md + WORKSPACE.md →
// EnsureProject per project in dependency order → WORKSPACE.md refreshed to
// list what was checked out. ANY failure runs the undo stack LIFO and reports
// the original error joined with any undo errors; the original error's kind
// picks the exit code. Worktree branches are deliberately NOT deleted by undo
// (decided table: a branch is the user's work, and the surviving branch is
// simply reused when the fixed `new` re-runs).
func newNewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "new <task_id> <description> [project...]",
		Short: "Create a workspace: allocate, scaffold docs, check projects out",
		// A task id plus a description; fewer is a usage error → exit 2 (spec §9).
		Args: usageArgs(cobra.MinimumNArgs(2)),
		// --json is inherited and deliberately unused: spec §2 scopes it to the
		// query commands. Accepting and ignoring it keeps `workspace --json new
		// T-1 desc` working for a caller that sets the flag globally, rather
		// than failing on a command with no query result to serialize.
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID, desc := args[0], args[1]
			// Allocate does not validate the id (it also serves M4 adoption);
			// the CLI is where the decided rule bites, as a usage error.
			if !wsp.ValidTaskID(taskID) {
				return xerr.Wrap(xerr.ErrUsage,
					fmt.Errorf("invalid task id %q (want ^[A-Za-z0-9][A-Za-z0-9._-]*$, at most 64 bytes)", taskID))
			}

			root, err := config.RootDir()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root) // ErrConfig → exit 4
			if err != nil {
				return err
			}

			// Validate every project name before touching anything,
			// deduplicating as we go (new T-1 x app app must not ensure twice).
			seen := make(map[string]bool, len(args)-2)
			names := make([]string, 0, len(args)-2)
			for _, name := range args[2:] {
				if _, ok := cfg.Projects[name]; !ok {
					return xerr.Wrap(xerr.ErrNotFound, fmt.Errorf("project %q is not configured", name))
				}
				if !seen[name] {
					seen[name] = true
					names = append(names, name)
				}
			}
			ordered, err := wsp.TopoOrder(cfg, names)
			if err != nil {
				return err
			}

			// The registry keys on absolute dirs; root may be relative when it
			// came from the environment, so normalize before allocating.
			//
			// This dir needs no containment gate of its own (destroy's removal
			// phase has one, because it reads a dir back OUT of the registry).
			// It is constructed, not read: DirName yields a single path
			// component — the task id already passed ValidTaskID above (no
			// separator, no leading dot, so never "." or ".."), and the slug
			// it may append is [a-z0-9-] only — so Join can add exactly one
			// level below root, and Abs of that stays under Abs(root). The
			// untrusted step is the round trip through .allocations.json, not
			// this one.
			dir, err := filepath.Abs(filepath.Join(root, wsp.DirName(taskID, desc)))
			if err != nil {
				return err
			}
			if _, err := os.Stat(dir); err == nil {
				return fmt.Errorf("%s already exists", dir)
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("checking %s: %w", dir, err)
			}

			// --- transaction ---------------------------------------------
			// One timestamp for the whole invocation, stamped here (libraries
			// take it as a parameter so they stay testable).
			now := time.Now().Format(time.RFC3339)
			a, err := alloc.Allocate(root, dir, taskID, desc, now)
			if err != nil {
				return err // names the conflicting dir; nothing to undo
			}
			var undo undoStack
			undo.push(func() error { return alloc.Release(root, dir) })
			fail := func(err error) error {
				// Original error first: its kind picks the exit code even
				// after joining (errors.Is walks every branch of a join).
				return errors.Join(err, undo.run())
			}

			if err := os.Mkdir(dir, 0o755); err != nil {
				return fail(err)
			}
			undo.push(func() error { return os.RemoveAll(dir) })

			ws := wsp.Workspace{Dir: dir, Alloc: a}
			if err := wsp.EnsureClaudeMD(ws); err != nil {
				return fail(err) // dir removal is undo enough for both docs
			}
			if err := wsp.WriteWorkspaceMD(cfg, ws); err != nil {
				return fail(err)
			}

			for _, name := range ordered {
				// The worktree undo is pushed BEFORE EnsureProject, guarded by
				// IsWorkTreeRoot: EnsureProject is a single call that
				// internally does worktree+env+setup, so a failure AFTER
				// creating the worktree must still undo it — while a failure
				// before (or a worktree never created) makes the undo a no-op.
				// Registering only "on success" would leak the worktree of the
				// very project that failed at its setup step.
				//
				// ROOT is the same question EnsureProject's own gate asks: with
				// the workspaces root nested inside an enclosing repo, the
				// looser IsWorkTree would call this undo on a dest that never
				// became a worktree, handing `git worktree remove --force` a
				// directory git does not know as one.
				dest := wsp.ProjectDir(ws, cfg, name)
				repo := cfg.Projects[name].Repo
				undo.push(func() error {
					if !gitx.IsWorkTreeRoot(dest) {
						return nil
					}
					// Containment gate before the force-removal (shared with
					// destroy): even though config validation rejects escaping
					// paths, nothing here may remove a dir outside the
					// workspace this invocation created.
					if err := assertInsideWorkspace(dir, dest); err != nil {
						return err
					}
					return gitx.WorktreeRemove(repo, dest, true)
				})
				if err := wsp.EnsureProject(cfg, ws, name); err != nil {
					return fail(err) // already prefixed `project "<name>": …`
				}
			}
			if len(ordered) > 0 {
				// Refresh so WORKSPACE.md lists the projects just checked out;
				// the pre-projects write only sees an empty workspace.
				if err := wsp.WriteWorkspaceMD(cfg, ws); err != nil {
					return fail(err)
				}
			}
			// --- transaction committed ------------------------------------

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "workspace %s created (index %d)\n", ws.Name(), a.Index)
			// ProjectStates reports exactly what is checked out here — the
			// projects this new just ensured — rendered as status renders
			// them, so the two commands cannot drift apart.
			for _, st := range wsp.ProjectStates(cfg, ws) {
				fmt.Fprintf(out, "  %s: %s\n", st.Name, statusProjectDetail(statusProject{
					Name:         st.Name,
					CheckedOut:   true,
					Branch:       st.Branch,
					SetupCurrent: st.SetupCurrent,
				}))
			}
			fmt.Fprintf(out, "hint: workspace cd %s\n", taskID)
			return nil
		},
	}
}
