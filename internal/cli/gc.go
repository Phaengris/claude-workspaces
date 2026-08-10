package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/gitx"
	"git.internal/cat/claude-workspaces-go/internal/proc"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

// newGCCmd builds `workspace gc [--destroy-dirs]`: garbage-collect the whole
// registry in up to three passes (spec §2's gc row).
//
//  1. release — every allocation whose dir no longer EXISTS is released, one
//     `released <name> (dir vanished)` line each. Existence is os.Stat
//     answering fs.ErrNotExist, and only that: any OTHER stat failure
//     (permissions, I/O) is not proof of absence, so the allocation is kept
//     and the error reported — releasing on doubt would leak a live
//     workspace's index. A workspace released here takes no further part in
//     the passes below.
//
//  2. reap — for every surviving workspace, every configured daemon of EVERY
//     configured project, checked out or not: a pid file that exists but does
//     not name a live process is removed, one `reaped stale pid file
//     <ws>/<project:daemon>` line each. All configured projects, because a
//     pid file lives under <ws>/.workspace/pids and can outlive its
//     project's worktree — a dead pid is stale garbage regardless of whether
//     the checkout still exists (and pass 3's daemon gate already scans the
//     same full set). "Not live" includes a CORRUPT pid file — DaemonState
//     already treats corrupt as not running (the decided Liveness row), and
//     a record nothing can act on is exactly the garbage this pass exists to
//     collect. A LIVE daemon's record is untouchable.
//
//  3. destroy (only with --destroy-dirs) — a surviving workspace is destroyed
//     via destroyWork (the full down + teardown + remove + release flow the
//     destroy command runs, force=false) when ALL of, gated cheapest-first
//     (registry field, then dir listing, then pid-file reads, then the
//     git-spawning checks):
//
//     - it is tool-created (Adopted=false; an adopted dir is never deleted,
//     the same rule destroy itself applies);
//     - at least one project is checked out — an EMPTY workspace has no
//     branches anywhere, hence no merge evidence at all, and gc destroys
//     only on evidence, so it SURVIVES. That covers the half-destroyed
//     case too: a workspace a partial destroy left with its worktrees
//     already removed reads as empty here and is skipped — finish it with
//     `workspace destroy` (or destroy --force), the "I mean it" path for
//     anything without evidence;
//     - no daemon of ANY configured project is running in it. All configured
//     projects, not just checked-out ones: a pid file can outlive its
//     project's worktree, and if it still names a live process that
//     process holds this index's ports — destroying around it would strand
//     it invisibly, the exact failure destroy's phase-0 exists to prevent;
//     - every checked-out project's branch <task_id> is fully merged into
//     that project's base_branch — gitx.IsMerged, where an empty
//     base_branch falls back to the source repo's own HEAD branch
//     (gitx.DefaultBranch), mirroring checkout's "branch from HEAD when
//     base is empty". Any unanswerable merge question (missing branch,
//     missing base, moved repo, detached HEAD) reads as NOT merged —
//     never destroy on doubt;
//     - no checked-out worktree is DIRTY — modified or untracked files, the
//     same porcelain question `ls -g` renders, via gitx.StatsFor. Merged
//     covers only what was COMMITTED; destroyWork's removal discards the
//     working copy, and uncommitted work is the wrong thing for a batch
//     sweep to discard — the explicit `workspace destroy` keeps that
//     power, gc does not (plan-owner ruling). A dirty workspace survives
//     LOUDLY, `skipped <name> (uncommitted changes)`: unlike the silent
//     gates above, it is the one case where everything else says
//     "collectable" except work saved nowhere. It is checked LAST so the
//     line fires only for otherwise-collectable workspaces — a dirty but
//     unmerged workspace survives silently like any other unmerged one.
//     An unanswerable dirty check reads dirty (doubt survives). Note the
//     shared predicate's consequence: checkout writes .env INTO the
//     worktree, so a project whose repo does not gitignore .env reads
//     dirty in every workspace — to `ls -g`'s star and to this gate alike
//     — and gc -d then collects none of them. Deliberate: one dirty
//     definition tool-wide, and the fix (ignore .env) lives in the repo.
//
//     Each destruction prints destroyWork's own lines and then
//     `destroyed <name> (merged)`.
//
// Failure policy is join-and-continue, per workspace: gc is a batch, and one
// workspace's broken teardown must not stop the vanished-dir release after
// it. Errors are collected (each prefixed `workspace <name>: `), the batch
// runs to completion, and the joined error exits 1 — flattened to a plain
// error on purpose, so a KINDED error inside the batch (an ErrNotFound from
// some inner resolution, say) cannot flip the whole batch's exit code to its
// own; per-workspace codes are meaningless once several workspaces' failures
// are one error. The summary line is always printed — `gc: N released,
// M reaped, K destroyed`, counting only successes — EXCEPT when there was
// truly nothing to do and nothing failed: then the single line `nothing to
// collect` (exit 0) says so instead.
func newGCCmd() *cobra.Command {
	var destroyDirs bool
	cmd := &cobra.Command{
		Use:   "gc [--destroy-dirs]",
		Short: "Collect garbage: vanished allocations, stale pid files, merged workspaces",
		// No positionals: gc always works on the whole registry (spec §2).
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, reg, err := loadRootDir()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			var errs []error
			var released, reaped, destroyed int

			// Pass 1 — release vanished dirs. wsp.List sorts by name, which is
			// what makes every pass's output (and the join order) stable.
			var survivors []wsp.Workspace
			for _, ws := range wsp.List(reg) {
				_, statErr := os.Stat(ws.Dir)
				switch {
				case statErr == nil:
					survivors = append(survivors, ws)
				case errors.Is(statErr, fs.ErrNotExist):
					if err := alloc.Release(root, ws.Dir); err != nil {
						errs = append(errs, fmt.Errorf("workspace %s: %w", ws.Name(), err))
						continue
					}
					released++
					fmt.Fprintf(out, "released %s (dir vanished)\n", ws.Name())
				default:
					// Unreadable is not vanished: report, keep the allocation,
					// and keep the workspace out of the destructive passes.
					errs = append(errs, fmt.Errorf("workspace %s: %w", ws.Name(), statErr))
				}
			}

			// Pass 2 — reap stale pid files, every configured project's
			// daemons (a dead pid is stale whether or not the checkout
			// still exists; see the doc comment).
			for _, ws := range survivors {
				for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
					for _, d := range wsp.DaemonsOf(cfg, name) {
						pidPath := wsp.PidPath(ws, d)
						pid, starttime, err := proc.ReadPidFile(pidPath)
						if errors.Is(err, fs.ErrNotExist) {
							continue // no record, nothing to reap
						}
						if err == nil && proc.Alive(pid, starttime) {
							continue // live daemon: untouchable
						}
						// Dead or corrupt — both are stale records.
						if rmErr := os.Remove(pidPath); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
							errs = append(errs, fmt.Errorf("workspace %s: %w", ws.Name(), rmErr))
							continue
						}
						reaped++
						fmt.Fprintf(out, "reaped stale pid file %s/%s\n", ws.Name(), d.Key())
					}
				}
			}

			// Pass 3 — destroy fully merged, clean, daemonless, tool-created
			// workspaces that actually contain something. Gate order per the
			// doc comment: cheap checks first, the loud dirty gate last.
			if destroyDirs {
				for _, ws := range survivors {
					if ws.Alloc.Adopted {
						continue // never delete a dir the tool did not create
					}
					states := wsp.ProjectStates(cfg, ws)
					if len(states) == 0 {
						continue // empty workspace: no merge evidence, survives
					}
					if anyDaemonRunning(cfg, ws) {
						continue
					}
					if !allMerged(cfg, ws, states) {
						continue
					}
					if anyDirty(states) {
						// Everything else says collectable; only unsaved work
						// stands in the way — the one skip worth a line.
						fmt.Fprintf(out, "skipped %s (uncommitted changes)\n", ws.Name())
						continue
					}
					if err := destroyWork(cmd, cfg, root, ws, false); err != nil {
						errs = append(errs, fmt.Errorf("workspace %s: %w", ws.Name(), err))
						continue
					}
					destroyed++
					fmt.Fprintf(out, "destroyed %s (merged)\n", ws.Name())
				}
			}

			if released+reaped+destroyed == 0 && len(errs) == 0 {
				fmt.Fprintln(out, "nothing to collect")
				return nil
			}
			fmt.Fprintf(out, "gc: %d released, %d reaped, %d destroyed\n", released, reaped, destroyed)
			if joined := errors.Join(errs...); joined != nil {
				// %v, not %w: deliberately sever the unwrap chain so no
				// kinded inner error can dictate the batch's exit code —
				// a batch failure is a plain error, exit 1 (doc comment).
				return fmt.Errorf("%v", joined)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&destroyDirs, "destroy-dirs", "d", false,
		"also destroy tool-created workspaces that are fully merged, have no uncommitted changes, and run no daemon")
	return cmd
}

// allMerged reports whether EVERY checked-out project's branch <task_id> is
// fully merged into that project's base. The base is the configured
// base_branch, or — when empty — the source repo's own HEAD branch
// (gitx.DefaultBranch), the same default WorktreeAdd branched from at
// checkout time. Any failure to answer (missing branch, unresolvable
// default, moved repo) is FALSE: gc destroys on evidence, never on doubt.
func allMerged(cfg *config.Config, ws wsp.Workspace, states []wsp.ProjectState) bool {
	for _, st := range states {
		p := cfg.Projects[st.Name]
		base := p.BaseBranch
		if base == "" {
			b, err := gitx.DefaultBranch(p.Repo)
			if err != nil {
				return false
			}
			base = b
		}
		if !gitx.IsMerged(p.Repo, ws.Alloc.TaskID, base) {
			return false
		}
	}
	return true
}

// anyDaemonRunning reports whether any configured project's daemon is alive
// in this workspace. ALL configured projects, not just checked-out ones: pid
// files live under <ws>/.workspace/pids and can outlive their project's
// worktree, and a live process found through one still holds this index's
// ports — gc must not destroy the workspace out from under it.
func anyDaemonRunning(cfg *config.Config, ws wsp.Workspace) bool {
	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		for _, d := range wsp.DaemonsOf(cfg, name) {
			if running, _ := wsp.DaemonState(ws, d); running {
				return true
			}
		}
	}
	return false
}

// anyDirty reports whether any of the checked-out worktrees holds
// uncommitted work — gitx.StatsFor's porcelain answer, the same predicate
// `ls -g` renders, reused rather than re-derived. A worktree whose state
// cannot be read (Stats.Err) also counts as dirty: the caller is deciding
// whether a batch sweep may DISCARD the working copy, and an unanswerable
// question must fail toward keeping it.
func anyDirty(states []wsp.ProjectState) bool {
	dirs := make([]string, len(states))
	for i, st := range states {
		dirs[i] = st.Dir
	}
	for _, s := range gitx.StatsFor(dirs, len(dirs)) {
		if s.Dirty || s.Err != nil {
			return true
		}
	}
	return false
}
