package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Phaengris/claude-workspaces/internal/alloc"
	"github.com/Phaengris/claude-workspaces/internal/config"
	"github.com/Phaengris/claude-workspaces/internal/gitx"
	"github.com/Phaengris/claude-workspaces/internal/proc"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
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
//  2. reap — for every surviving workspace, every pid file in its
//     <ws>/.workspace/pids DIRECTORY: one that does not name a live process is
//     removed, one `reaped stale pid file <ws>/<project:daemon>` line each. The
//     directory, not the config's daemon list: a pid file is named after the key
//     `up` wrote it under, so a daemon renamed, dropped from `start:`, or whose
//     project left the config leaves a record no config-driven walk can even
//     see — and that record is precisely the garbage most in need of collecting
//     (pass 3's daemon gate reads the same directory, for the mirror-image
//     reason). "Not live" includes a CORRUPT or unreadable pid file — a record
//     nothing can act on cannot name a daemon anyone could stop, which is the
//     decided Liveness row exactly. A LIVE daemon's record is untouchable. An
//     unreadable pids DIRECTORY is a per-workspace error (below), not a licence
//     to assume it is empty.
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
//     - NOTHING recorded in its pids directory is still running
//     (anyDaemonRunning). Every record found on disk, not the configured
//     daemons: a pid file outlives its project's worktree and its own
//     config entry, and while it still names a live process that process
//     holds this index's ports — destroying around it would strand it
//     invisibly, the exact failure destroy's phase-0 exists to prevent. An
//     unanswerable gate (unreadable pids dir) is an error and a skip, never
//     an assumed quiet;
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
		// --json is inherited and deliberately unused: spec §2 scopes it to the
		// query commands. Accepting and ignoring it keeps `workspace --json gc`
		// working for a caller that sets the flag globally.
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

			// Pass 2 — reap stale pid files: every record IN THE PIDS
			// DIRECTORY, whatever key it carries (see the doc comment).
			for _, ws := range survivors {
				keys, err := wsp.PidFileKeys(ws)
				if err != nil {
					errs = append(errs, fmt.Errorf("workspace %s: %w", ws.Name(), err))
					continue
				}
				for _, key := range keys {
					pidPath := filepath.Join(wsp.PidsDir(ws), key)
					pid, starttime, err := proc.ReadPidFile(pidPath)
					if errors.Is(err, fs.ErrNotExist) {
						continue // vanished under us, nothing to reap
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
					fmt.Fprintf(out, "reaped stale pid file %s/%s\n", ws.Name(), key)
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
					running, err := anyDaemonRunning(ws)
					if err != nil {
						// The gate could not be answered: report and skip.
						// An unreadable pids dir is not evidence of quiet.
						errs = append(errs, fmt.Errorf("workspace %s: %w", ws.Name(), err))
						continue
					}
					if running {
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
			return flattenBatch(errors.Join(errs...))
		},
	}
	cmd.Flags().BoolVarP(&destroyDirs, "destroy-dirs", "d", false,
		"also destroy tool-created workspaces that are fully merged, have no uncommitted changes, and run no daemon")
	return cmd
}

// flattenBatch turns a batch's joined per-workspace errors into a PLAIN error
// — same text, no unwrap chain — and keeps nil as nil.
//
// %v, not %w, deliberately: a kinded error raised inside one workspace's work
// (an xerr.ErrNotFound from some inner resolution, say) would otherwise make
// errors.Is match on the whole batch and hand the command that kind's exit code.
// Per-workspace codes are meaningless once several workspaces' failures are one
// error, so a batch failure is exit 1 and nothing else — gc's documented failure
// policy, pinned by TestFlattenBatchSeversKinds.
func flattenBatch(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%v", err)
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

// anyDaemonRunning reports whether ANY daemon record in this workspace still
// names a live process — the shared "something is running here" gate, used by
// gc's destroy pass and by `release`.
//
// It enumerates wsp.PidsDir, NOT the config. The config only knows the daemons
// configured RIGHT NOW, and a pid file is named after the key `up` wrote it
// under: rename a daemon, drop it from `start:`, or remove its project, and its
// record becomes invisible to any config-driven walk while the process it names
// keeps holding this workspace's ports. Both callers are about to do something
// irreversible around that process (delete the directory, free the index), so
// the inventory has to be the one place that cannot lie: the directory itself.
//
// A missing pids directory is "nothing runs here" (a workspace that never ran
// `up`), not an error. Any OTHER failure to read the directory IS an error, and
// deliberately not folded into either answer: "cannot tell" must reach the
// caller so it can refuse rather than guess quiet. Per-file failures are not
// errors, though — an unreadable or corrupt record cannot name a live process,
// which is exactly what DaemonState's Liveness row already says, and pass 2
// treats such a record as the garbage it is.
func anyDaemonRunning(ws wsp.Workspace) (bool, error) {
	keys, err := wsp.PidFileKeys(ws)
	if err != nil {
		return false, err
	}
	for _, key := range keys {
		pid, starttime, err := proc.ReadPidFile(filepath.Join(wsp.PidsDir(ws), key))
		if err == nil && proc.Alive(pid, starttime) {
			return true, nil
		}
	}
	return false, nil
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
