package cli

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/gitx"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

// newAdoptCmd builds `workspace adopt [dir] [--projects a,b]`: give an
// EXISTING directory an allocation, so a tree the user assembled by hand (or
// v1 left behind, or a colleague cloned) gets an index, values, a .env per
// project, and shows up in every command that reads the registry.
//
// Nothing is created and nothing is cloned: adopt does not add worktrees, does
// not run setup, and does not move anything. It writes exactly three kinds of
// file into the dir — a .env per adopted project, WORKSPACE.md, and CLAUDE.md
// if absent — and records Adopted=true, which is what tells `destroy` and `gc`
// that this directory is not theirs to delete (spec §2).
//
// Flag parsing is NORMAL here, unlike the session commands: adopt runs nothing
// and forwards nothing, so `--projects` is an ordinary cobra flag and an
// unknown flag is an ordinary usage error.
//
// Check order — everything that can refuse runs before ANY mutation:
//
//  1. dir: defaults to the cwd, always made absolute (the registry keys on
//     absolute paths). It must exist and be a directory, and it must not BE
//     the workspaces root — the root is the container of workspaces, and
//     allocating it would make every workspace live inside a workspace.
//     Plain errors, exit 1: these are facts about the world, not typos.
//  2. task id = the directory's base name, verbatim, which must satisfy
//     ValidTaskID. Deliberately NOT slugged: the id is how every other command
//     addresses this workspace, and silently adopting `my stuff` as `my-stuff`
//     would mean the name on disk and the name in the tool disagree forever.
//     Plain error (exit 1) naming the basename — the user typed a directory,
//     not an id, so `new`'s usage-error framing would be misleading.
//  3. --projects, if given: every name must be configured, exit 3, on the same
//     rule and message as checkout/env/cd. This runs BEFORE the
//     already-adopted no-op below so a typo is never swallowed by an
//     idempotent success.
//  4. the registry: an existing allocation for this exact dir is either the
//     idempotent case (adopted → print and exit 0, spec §2 calls adopt
//     idempotent) or a conflict (a tool-created workspace → plain error, exit
//     1, naming its task). A DIFFERENT directory whose basename collides on
//     the task id is still a conflict, reported by Allocate itself.
//
// Project selection is detection unless --projects overrides it ENTIRELY (the
// flag replaces the detected set, it does not extend it): for each configured
// project, this dir is adopting it iff gitx.IsWorkTreeRoot of the project's
// path inside the dir — git's own answer, the same predicate ProjectStates
// uses, so adopt and status can never disagree about what is checked out here.
func newAdoptCmd() *cobra.Command {
	var projects []string
	cmd := &cobra.Command{
		Use:   "adopt [dir]",
		Short: "Adopt an existing directory as a workspace (allocate in place, never create)",
		// At most one positional; more is a usage error → exit 2 (spec §9).
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, reg, err := loadRootDir()
			if err != nil {
				return err
			}
			dir, err := adoptDir(args)
			if err != nil {
				return err
			}
			if err := checkAdoptable(root, dir); err != nil {
				return err
			}
			taskID := filepath.Base(dir)
			if !wsp.ValidTaskID(taskID) {
				return fmt.Errorf("%s: directory name %q is not usable as a task id "+
					"(want ^[A-Za-z0-9][A-Za-z0-9._-]*$, at most 64 bytes); rename the directory or create a workspace with `workspace new`",
					dir, taskID)
			}

			// Selection before the registry check: a bad project name must
			// fail loudly even for a dir that is already adopted.
			selected, err := adoptProjects(cfg, wsp.Workspace{Dir: dir}, projects)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if existing, ok := allocationAt(reg, dir); ok {
				if existing.Alloc.Adopted {
					// Idempotent: the allocation this run would have made is
					// already there, with the same index and the same values,
					// so there is nothing to do and nothing to report but that.
					fmt.Fprintf(out, "already adopted (#%d)\n", existing.Alloc.Index)
					return nil
				}
				return fmt.Errorf("%s is already allocated to task %s", dir, existing.Alloc.TaskID)
			}

			// One timestamp for the whole invocation (libraries take it as a
			// parameter so they stay testable). The description is empty: adopt
			// takes none, and `ls` marks the row `(adopted)` instead.
			now := time.Now().Format(time.RFC3339)
			a, err := alloc.AllocateAdopted(root, dir, taskID, "", now)
			if err != nil {
				return err // names the conflicting dir or task id; nothing to undo
			}
			ws := wsp.Workspace{Dir: dir, Alloc: a}
			// The allocation is adopt's ONLY undo. The files below land in a
			// directory the tool did not create, so it will not remove them —
			// but leaving the allocation behind after a half-done adopt would
			// be worse than useless: the retry would hit the "already adopted"
			// no-op above and never redo the writes. Releasing means a re-run
			// is a full re-run.
			fail := func(err error) error {
				return errors.Join(err, alloc.Release(root, dir))
			}
			for _, name := range selected {
				if err := wsp.WriteEnvFile(cfg, ws, name); err != nil {
					return fail(fmt.Errorf("project %q: %w", name, err))
				}
			}
			if err := wsp.WriteWorkspaceMD(cfg, ws); err != nil {
				return fail(err)
			}
			if err := wsp.EnsureClaudeMD(ws); err != nil {
				return fail(err)
			}

			fmt.Fprintf(out, "adopted %s #%d\n", ws.Name(), a.Index)
			for _, name := range selected {
				fmt.Fprintf(out, "  %s: .env written\n", name)
			}
			if len(selected) == 0 {
				// Not an error: an adopted dir is allowed to hold no projects
				// yet (checkout can add them later). Saying so beats a silent
				// one-line success that looks like detection worked.
				fmt.Fprintln(out, "  no configured projects checked out here")
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&projects, "projects", nil,
		"adopt these configured projects instead of detecting them (comma-separated)")
	return cmd
}

// adoptDir resolves adopt's optional positional to an absolute path: the
// argument when given, otherwise the current directory (the common case —
// `cd` somewhere and adopt it).
func adoptDir(args []string) (string, error) {
	dir := ""
	if len(args) == 1 {
		dir = args[0]
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = cwd
	}
	return filepath.Abs(dir)
}

// checkAdoptable applies the two refusals that are about the directory itself:
// it must exist and be a directory, and it must not be the workspaces root.
// Both are plain errors (exit 1).
//
// The root check compares cleaned absolute paths — config.RootDir may return a
// relative path (it comes from the environment) — and refuses ONLY the root
// itself. A directory anywhere else is fair game, including one nested inside
// the root: a v1 leftover sitting among tool-created workspaces is exactly the
// thing adopt exists to reclaim.
func checkAdoptable(root, dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("cannot adopt %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("cannot adopt %s: not a directory", dir)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if filepath.Clean(dir) == filepath.Clean(rootAbs) {
		return fmt.Errorf("refusing to adopt %s: that is the workspaces root itself, not a workspace", dir)
	}
	return nil
}

// adoptProjects decides which projects this adoption covers, sorted by name.
//
// With --projects the named set wins outright — detection is not consulted at
// all, so a user can adopt a subset of what is on disk, or claim a project dir
// git does not recognize as a work tree. Names are validated by the shared
// resolveProjectNames (unconfigured → exit 3, deduped), then sorted: adopt
// only writes per-project .env files, which are independent, so dependency
// order buys nothing and a stable alphabetical order makes the output
// diffable.
//
// Without the flag, detection: the configured projects whose directory inside
// ws is a work tree ROOT. Root, not merely "inside a work tree" — with the
// adopted dir sitting inside some enclosing repo, the looser predicate would
// claim every configured project as present (ProjectStates' rationale).
func adoptProjects(cfg *config.Config, ws wsp.Workspace, flag []string) ([]string, error) {
	if len(flag) > 0 {
		named, err := resolveProjectNames(cfg, flag)
		if err != nil {
			return nil, err
		}
		slices.Sort(named)
		return named, nil
	}
	var detected []string
	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		if gitx.IsWorkTreeRoot(wsp.ProjectDir(ws, cfg, name)) {
			detected = append(detected, name)
		}
	}
	return detected, nil
}
