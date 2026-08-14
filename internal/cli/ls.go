package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Phaengris/claude-workspaces/internal/alloc"
	"github.com/Phaengris/claude-workspaces/internal/config"
	"github.com/Phaengris/claude-workspaces/internal/gitx"
	"github.com/Phaengris/claude-workspaces/internal/ui"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
)

// gitStatWorkers bounds the concurrent `git` invocations behind `ls -g`
// (spec §7). One StatsFor call covers every checked-out project of every
// workspace, so the bound is global rather than per workspace.
const gitStatWorkers = 8

// noWorkspaces is the human-output line for an empty registry. It is not an
// error: a fresh root legitimately has no workspaces, so both ls and ports
// print it and exit 0. The --json paths emit an empty container instead, so a
// machine consumer never has to special-case a prose line.
const noWorkspaces = "no workspaces"

// lsProject is one checked-out project as `ls -g` reports it.
type lsProject struct {
	Name   string `json:"name"`
	Branch string `json:"branch"`
	Dirty  bool   `json:"dirty"`
}

// lsEntry is one workspace in `ls --json`. It flattens the allocation (which is
// the registry's own JSON shape) next to the derived dir/name so a consumer
// needs no second lookup.
//
// Projects is a POINTER to a slice on purpose: without -g it must be absent
// (nothing was measured), while with -g it must be present even when the
// workspace has no checked-out project — and `omitempty` cannot tell an empty
// slice from a nil one.
type lsEntry struct {
	Dir         string       `json:"dir"`
	Name        string       `json:"name"`
	Index       int          `json:"index"`
	TaskID      string       `json:"task_id"`
	Description string       `json:"description"`
	CreatedAt   string       `json:"created_at"`
	Adopted     bool         `json:"adopted"`
	Projects    *[]lsProject `json:"projects,omitempty"`
}

// newLsCmd builds `workspace ls`: every workspace in the registry, one line
// each, sorted by name. Config errors pass through unwrapped (Load already tags
// them ErrConfig → exit 4); a registry that exists but cannot be read is a
// plain error → exit 1.
func newLsCmd() *cobra.Command {
	var withGit, all bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List all workspaces",
		Args:  usageArgs(cobra.NoArgs), // extra args are a usage error → exit 2 (spec §9)
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := cmd.Flags().GetBool("json")
			if err != nil {
				return err
			}
			root, cfg, reg, err := loadRootDir()
			if err != nil {
				return err
			}
			entries := lsEntries(cfg, reg, withGit)
			var extra []lsUnregistered
			if all {
				extra = unregisteredDirs(root, reg)
			}

			out := cmd.OutOrStdout()
			if asJSON {
				if !all {
					return ui.PrintJSON(out, entries)
				}
				// One mixed array, name-sorted like the table: registered
				// entries keep their exact shape (the pre--a contract),
				// unregistered ones carry only what is derivable.
				combined := make([]any, 0, len(entries)+len(extra))
				for _, e := range entries {
					combined = append(combined, e)
				}
				for _, u := range extra {
					combined = append(combined, u)
				}
				return ui.PrintJSON(out, combined)
			}
			if len(entries) == 0 && len(extra) == 0 {
				fmt.Fprintln(out, noWorkspaces)
				return nil
			}
			rows := make([][]string, 0, len(entries)+len(extra)+1)
			for _, e := range entries {
				rows = append(rows, lsRow(e))
			}
			for _, u := range extra {
				rows = append(rows, unregisteredRow(u))
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
			rows = append([][]string{lsHeader(withGit)}, rows...)
			ui.Table(out, rows)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&withGit, "git", "g", false, "append per-project git branch and dirty state")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "also list root dirs without an allocation (released workspaces, strangers)")
	return cmd
}

// lsUnregistered is a directory in the root that no allocation claims, as
// `ls -a` reports it. Status is "released" — the dir carries the tool's
// .workspace footprint, so it is what `release` leaves behind (v1 called this
// archived) — or "unmanaged", a stranger. TaskID is DERIVED from the name's
// `<task>_<slug>` shape the tool itself builds (released dirs only); there is
// no description to report — the allocation owned it and the allocation is
// gone. The slug shown in the human row is the description's shadow, not the
// description.
type lsUnregistered struct {
	Dir    string `json:"dir"`
	Name   string `json:"name"`
	TaskID string `json:"task_id,omitempty"`
	Status string `json:"status"`
}

// unregisteredDirs scans the root for directories no allocation claims —
// derived visibility for what `release` leaves behind, with no registry
// involvement (the decided fork: an archived workspace is a CONDITION of the
// world, never a recorded status). Dot-entries are machine-owned or hidden
// (.allocations.json's own temp files, .lock), non-dirs are config.yml and
// friends; both are skipped. A scan failure yields nothing: `ls -a` degrades
// to plain `ls` rather than failing the listing that mattered.
func unregisteredDirs(root string, reg alloc.Registry) []lsUnregistered {
	dirents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	registered := make(map[string]bool, len(reg))
	for _, ws := range wsp.List(reg) {
		registered[ws.Name()] = true
	}
	var out []lsUnregistered
	for _, d := range dirents {
		name := d.Name()
		if !d.IsDir() || strings.HasPrefix(name, ".") || registered[name] {
			continue
		}
		u := lsUnregistered{Dir: filepath.Join(root, name), Name: name, Status: "unmanaged"}
		if fi, err := os.Stat(filepath.Join(root, name, ".workspace")); err == nil && fi.IsDir() {
			u.Status = "released"
			u.TaskID, _, _ = strings.Cut(name, "_")
		}
		out = append(out, u)
	}
	return out
}

// unregisteredRow renders one unregistered dir as table cells shaped like
// lsRow's: '-' where an allocation would have answered, and the label that
// says what this dir is and how to act on it.
func unregisteredRow(u lsUnregistered) []string {
	if u.Status != "released" {
		return []string{u.Name, "-", "-", "(unmanaged)"}
	}
	_, slug, _ := strings.Cut(u.Name, "_")
	desc := strings.TrimSpace(slug + " (released — workspace adopt to reuse)")
	return []string{u.Name, "-", u.TaskID, desc}
}

// lsEntries derives the full listing once, for both renderings — the human
// table and --json read the same values, so the two can never disagree.
//
// EVERY git touch lives inside the withGit branch, and must stay there. Plain
// `ls` is pure registry data (the allocation plus the dir's base name), so it
// costs zero subprocesses no matter how large the root is. Note that
// wsp.ProjectStates is itself a git caller — IsWorkTreeRoot + Branch per
// configured project, serially — so deriving states unconditionally would make plain `ls`
// on a 10-workspace × 5-project root spawn ~100 git processes outside the
// gitStatWorkers bound and then print none of it. The stats behind -g are
// fetched in a SINGLE bounded StatsFor call over every workspace's checked-out
// project dirs, rather than one call per workspace, so the bound holds globally
// and the walk is one wave (spec §7).
func lsEntries(cfg *config.Config, reg alloc.Registry, withGit bool) []lsEntry {
	all := wsp.List(reg) // sorted by name; the output order for both renderings
	var (
		states [][]wsp.ProjectState // nil unless withGit
		stats  map[string]gitx.Stats
	)
	if withGit {
		states = make([][]wsp.ProjectState, len(all))
		var dirs []string
		for i, ws := range all {
			states[i] = wsp.ProjectStates(cfg, ws)
			for _, st := range states[i] {
				dirs = append(dirs, st.Dir)
			}
		}
		stats = gitx.StatsFor(dirs, gitStatWorkers)
	}

	entries := make([]lsEntry, 0, len(all))
	for i, ws := range all {
		e := lsEntry{
			Dir:         ws.Dir,
			Name:        ws.Name(),
			Index:       ws.Alloc.Index,
			TaskID:      ws.Alloc.TaskID,
			Description: ws.Alloc.Description,
			CreatedAt:   ws.Alloc.CreatedAt,
			Adopted:     ws.Alloc.Adopted,
		}
		if withGit {
			// Non-nil even when empty: see lsEntry.Projects.
			projects := make([]lsProject, 0, len(states[i]))
			for _, st := range states[i] {
				p := lsProject{Name: st.Name, Branch: st.Branch}
				// A per-dir git failure (Stats.Err) leaves branch/dirty as
				// ProjectStates saw them — a listing degrades, never fails.
				if s, ok := stats[st.Dir]; ok && s.Err == nil {
					p.Branch, p.Dirty = s.Branch, s.Dirty
				}
				projects = append(projects, p)
			}
			e.Projects = &projects
		}
		entries = append(entries, e)
	}
	return entries
}

// adoptedMarker flags an adopted workspace in human output. It is appended to
// the DESCRIPTION cell rather than given a column of its own, so the row shape
// is unchanged for every existing consumer: `-g` project cells still start at
// index 4, and a plain listing stays four columns wide. --json is where a
// machine consumer reads it, from the `adopted` field that has always been
// there.
const adoptedMarker = "(adopted)"

// lsHeader labels lsRow's fixed columns — a row like any other, aligned by
// the same tabwriter, uppercase and undecorated (no ANSI: piped output stays
// byte-clean). `-g`'s per-project cells vary in count per row, so they get
// ONE trailing PROJECTS label over the first rather than a column each; the
// cells are self-describing (name@branch). The empty listing keeps its prose
// "no workspaces" line INSTEAD of a lone header: labels with nothing under
// them would dress an empty room.
func lsHeader(withGit bool) []string {
	h := []string{"WORKSPACE", "INDEX", "TASK", "DESCRIPTION"}
	if withGit {
		h = append(h, "PROJECTS")
	}
	return h
}

// lsRow renders one entry as table cells: NAME  #INDEX  TASK_ID  DESCRIPTION,
// plus one PROJECT@BRANCH cell per checked-out project (with a '*' suffix when
// the tree is dirty) for `-g`. Exported package-privately because `status`
// without a workspace argument reuses this exact one-line format.
//
// An adopted workspace's description cell carries adoptedMarker — usually
// alone, since `adopt` takes no description.
func lsRow(e lsEntry) []string {
	desc := e.Description
	if e.Adopted {
		desc = strings.TrimSpace(desc + " " + adoptedMarker)
	}
	row := []string{e.Name, "#" + strconv.Itoa(e.Index), e.TaskID, desc}
	if e.Projects == nil {
		return row
	}
	for _, p := range *e.Projects {
		cell := p.Name + "@" + p.Branch
		if p.Dirty {
			cell += "*"
		}
		row = append(row, cell)
	}
	return row
}

// loadRoot is the shared preamble of every read-only command: resolve the root,
// load+validate the config, read the registry. Config failures keep their
// ErrConfig kind (exit 4); a registry that cannot be read is a plain error
// (exit 1) prefixed so the message names its source.
func loadRoot() (*config.Config, alloc.Registry, error) {
	_, cfg, reg, err := loadRootDir()
	return cfg, reg, err
}

// loadRootDir is loadRoot for the callers that also need the ROOT itself —
// `launch`, whose create path hands it to newWork (allocation writes there).
// Same preamble, same error kinds; loadRoot is the two-thirds view of it, so
// there is only ever one load order.
func loadRootDir() (string, *config.Config, alloc.Registry, error) {
	root, err := config.RootDir()
	if err != nil {
		return "", nil, nil, err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return "", nil, nil, err
	}
	reg, err := alloc.Load(root)
	if err != nil {
		return "", nil, nil, fmt.Errorf("registry: %w", err)
	}
	return root, cfg, reg, nil
}
