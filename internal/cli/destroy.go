package cli

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/Phaengris/claude-workspaces/internal/alloc"
	"github.com/Phaengris/claude-workspaces/internal/config"
	"github.com/Phaengris/claude-workspaces/internal/gitx"
	"github.com/Phaengris/claude-workspaces/internal/proc"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
)

// newDestroyCmd builds `workspace destroy [--force] <workspace>`: stop every
// daemon, run every checked-out project's teardown commands, then remove the
// worktrees, the workspace dir, and the allocation — spec §2's
// "down + teardown + remove + release". The phased work itself lives in
// destroyWork, shared with `gc --destroy-dirs` (the decided gc row: reuse,
// don't duplicate).
//
// --force is the moved-repo escape hatch (the M2/M3-deferred decision): it
// downgrades WORKTREE-REMOVAL failures to warnings so a moved or deleted
// source repo cannot wedge destruction forever. It is precisely scoped to
// broken REPOS, not to skipping safety: the containment gates still refuse a
// corrupted registry entry, and down/teardown failures still abort — a daemon
// that survives or a teardown that fails is user state that force has no
// business discarding. See destroyWork for the mechanics.
func newDestroyCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "destroy [--force] <workspace>",
		Short: "Tear a workspace down: teardown commands, worktrees, dir, allocation",
		// Exactly one identifier; anything else is a usage error → exit 2 (spec §9).
		Args: usageArgs(cobra.ExactArgs(1)),
		// --json is inherited and deliberately unused: spec §2 scopes it to the
		// query commands. Accepting and ignoring it keeps `workspace --json
		// destroy X` working for a caller that sets the flag globally, rather
		// than failing on a command with no query result to serialize.
		RunE: func(cmd *cobra.Command, args []string) error {
			root, cfg, reg, err := loadRootDir()
			if err != nil {
				return err
			}
			ws, err := wsp.Resolve(reg, args[0]) // ErrNotFound → exit 3
			if err != nil {
				return err
			}
			return destroyWork(cmd, cfg, root, ws, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"downgrade worktree-removal failures to warnings (escape hatch for moved/broken source repos)")
	return cmd
}

// destroyWork is destroy's whole phased flow for an already-resolved
// workspace, shared by the destroy command and gc --destroy-dirs.
//
// A registry-containment gate runs FIRST, ahead of every phase: the resolved
// ws.Dir must be strictly inside the workspaces root (adopted workspaces
// exempted). A registry key is data, and both the down phase and teardown
// already act on it — see the gate itself for why "before RemoveAll" was not
// early enough. force does NOT bypass this gate: force is about repos that
// broke, never about trusting a registry entry less checked.
//
// Then three strict phases, because down and teardown are the parts that can
// fail for user reasons (spec §7):
//
//  0. down — the WHOLE workspace's daemons, via the same downAll flow a
//     no-target `down` runs (ResolveTargets with no filter, then the pids
//     DIRECTORY). This is not optional tidiness: destroy deletes the pid
//     records along with the dir, so a daemon that outlives destroy is
//     invisible AND still holding this index's ports, which the next workspace
//     on that index then cannot bind. The directory, not just the config, for
//     the same reason: a key that drifted out of the config (renamed daemon,
//     dropped `start:` entry, deleted project) still names a live process, and
//     it is exactly the one a config-driven stop would miss and this phase
//     would then erase all trace of. Any stop failure — or an unreadable pids
//     directory, which leaves what is running unknown — aborts destroy with
//     the joined errors and NOTHING removed, on the same convergence rule as
//     teardown below: a daemon that survived even SIGKILL keeps its pid record
//     (down's caller-removes contract) and its dir, so a re-run finds it and
//     tries again. force does not soften this phase: a live daemon is not a
//     broken repo.
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
//     converges on whatever failed. force does not soften this phase either:
//     teardown is the user's own cleanup, and skipping it silently would
//     leave state (containers, databases) the user asked to have torn down.
//  2. removal — only after every teardown succeeded: each worktree is removed
//     (force-removed in git's sense — destroy discards the working copy; the
//     BRANCH survives, it is the user's work), gated by
//     assertInsideWorkspace so no configuration can ever point removal
//     outside the workspace dir; then the workspace dir itself (already
//     cleared by the up-front containment gate); then the allocation, freeing
//     the index.
//
// Removal candidates are the checked-out projects (git's own answer) PLUS any
// configured project whose dir carries a `.git` regular FILE without being a
// healthy worktree root — git's linked-worktree marker with a broken other
// end. That is exactly what a moved or deleted source repo leaves behind:
// the project vanishes from ProjectStates (its git commands all fail), so
// without this widening destroy would silently RemoveAll a directory git
// still has bookkeeping for and report nothing wrong. A `.git` DIRECTORY (a
// standalone repo the tool never creates) stays a plain-content case for
// RemoveAll, as before. For each candidate:
//
//   - without --force, a WorktreeRemove failure aborts destroy (nothing
//     removed, the pinned behavior — a broken repo is surfaced, not papered
//     over);
//   - with --force, the failure becomes a `warning: worktree removal failed
//     for <dest>: …` line and removal proceeds; after the workspace dir is
//     gone, `git worktree prune` runs best-effort against every involved
//     source repo (prune failures are warnings too — with the repo itself
//     missing there is nothing left to clean).
//
// An ADOPTED workspace is a dir the tool did not create, so the tool will not
// delete it: down + teardown + release only, worktrees and dir left in place,
// and the output says so. Its daemons are ours — we started them — so they
// are stopped like any other's; only the REMOVAL is what adoption exempts.
func destroyWork(cmd *cobra.Command, cfg *config.Config, root string, ws wsp.Workspace, force bool) error {
	// Registry-containment gate, BEFORE anything acts on ws.Dir.
	// ws.Dir is a raw registry key and nothing upstream constrains it
	// to live under the workspaces root — a hand-edited or corrupted
	// .allocations.json could point it at ANY directory.
	//
	// This runs ahead of teardown, not just ahead of os.RemoveAll,
	// because teardown is not a read-only preamble: it spawns the
	// user's own commands with cwd and ${WORKSPACE} substitution
	// derived from THIS SAME unvalidated entry. Checking for corruption
	// only after those commands have run is checking too late — a
	// corrupt entry must stop destroy before it does anything at all.
	// (The per-worktree gate further down is a different question —
	// dest inside ws.Dir — and never fires for a workspace with
	// nothing checked out.)
	//
	// ADOPTED workspaces are exempt: adoption is precisely the case of
	// a directory the tool did not create, living wherever the user
	// already had it, so being outside the root is normal there. Their
	// teardown and release are legitimate, and their dir is never
	// removed at all.
	//
	// Both sides through filepath.Abs first — config.RootDir may be
	// relative (it comes from the environment; new.go normalizes with
	// Abs for exactly this reason) and isAncestorOrSame treats mixed
	// relative/absolute inputs as incomparable.
	if !ws.Alloc.Adopted {
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
	}

	// Phase 0 — down, the whole workspace, before anything is torn
	// down or removed. nil targets is `down <ws>` with no filter, and
	// downAll is that no-filter down in full: the config-resolved
	// daemons AND every live record in the pids DIRECTORY, config-known
	// or not. The directory is what makes this phase's promise true —
	// destroy deletes the records along with the dir and frees the
	// index, so a daemon whose key drifted out of the config would
	// otherwise survive with nothing left on disk to find it by, and no
	// re-run could ever converge on it.
	//
	// downAll on an EMPTY workspace is a silent no-op success: the
	// "nothing checked out" hint lives in down's own RunE, not in
	// downAll, so destroying a projectless workspace stays quiet and
	// down's UX is untouched.
	work, err := wsp.ResolveTargets(cfg, ws, nil)
	if err != nil {
		return err
	}
	if _, err := downAll(cmd, cfg, ws, work); err != nil {
		// Nothing removed, nothing torn down: a surviving daemon keeps
		// its pid record, so a re-run converges. Aborting here is also
		// what keeps teardown from running against a still-live app.
		// An UNREADABLE pids directory aborts too (downAll reports it):
		// destroy is irreversible, and "cannot tell what runs here" is
		// not a state to delete a workspace around.
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

	// The registry-containment gate ran up front, before teardown;
	// what remains here is the per-worktree gate (dest inside ws.Dir).
	// removalCandidates widens `ordered` with broken linked worktrees —
	// see the function's comment for why RemoveAll alone is not honest
	// there.
	candidates := removalCandidates(cfg, ws, ordered)
	var repos []string // involved source repos, deduped, candidate order
	seen := map[string]bool{}
	for _, name := range candidates {
		dest := wsp.ProjectDir(ws, cfg, name)
		if err := assertInsideWorkspace(ws.Dir, dest); err != nil {
			return err // force never bypasses containment
		}
		repo := cfg.Projects[name].Repo
		if !seen[repo] {
			seen[repo] = true
			repos = append(repos, repo)
		}
		if err := gitx.WorktreeRemove(repo, dest, true); err != nil {
			if !force {
				return fmt.Errorf("project %q: %w", name, err)
			}
			fmt.Fprintf(out, "warning: worktree removal failed for %s: %v\n", dest, err)
			continue
		}
		fmt.Fprintf(out, "project %q: worktree removed\n", name)
	}
	if err := os.RemoveAll(ws.Dir); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s removed\n", ws.Dir)
	if force {
		// Best-effort epilogue: with the dirs now gone, prune drops whatever
		// stale worktree bookkeeping the failed removals left in the source
		// repos, freeing the worktree names and branch locks. Failures are
		// warnings — the moved-repo case prunes nothing because the repo
		// path itself is gone, and that must not block the release below.
		for _, repo := range repos {
			if err := gitx.WorktreePrune(repo); err != nil {
				fmt.Fprintf(out, "warning: worktree prune failed for %s: %v\n", repo, err)
			}
		}
	}
	if err := alloc.Release(root, ws.Dir); err != nil {
		return err
	}
	fmt.Fprintf(out, "allocation released (task %s)\n", ws.Alloc.TaskID)
	return nil
}

// removalCandidates lists the projects destroy's removal phase must run
// WorktreeRemove for: the checked-out ones (ordered, teardown's topo order)
// plus — appended in sorted-name order — every other configured project whose
// dir inside the workspace carries a `.git` REGULAR FILE. That file is git's
// linked-worktree marker; a dir that has one without being a healthy worktree
// root is a checkout whose other end broke (source repo moved or deleted,
// marker corrupted), which ProjectStates cannot report because every git
// command there fails. Skipping such a dir would let plain destroy silently
// RemoveAll a directory git still holds bookkeeping for; attempting the
// removal surfaces an honest error instead (or a warning under --force).
//
// A `.git` DIRECTORY is deliberately NOT a candidate: that is a standalone
// repo, something the tool never creates, and `git worktree remove` could
// only ever refuse it — it stays plain workspace content for RemoveAll,
// exactly as before this widening.
func removalCandidates(cfg *config.Config, ws wsp.Workspace, ordered []string) []string {
	candidates := slices.Clone(ordered)
	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		if slices.Contains(ordered, name) {
			continue
		}
		fi, err := os.Lstat(filepath.Join(wsp.ProjectDir(ws, cfg, name), ".git"))
		if err == nil && fi.Mode().IsRegular() {
			candidates = append(candidates, name)
		}
	}
	return candidates
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
