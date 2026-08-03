package cli

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/gitx"
	"git.internal/cat/claude-workspaces-go/internal/ui"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
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
	var withGit bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List all workspaces",
		Args:  usageArgs(cobra.NoArgs), // extra args are a usage error → exit 2 (spec §9)
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := cmd.Flags().GetBool("json")
			if err != nil {
				return err
			}
			cfg, reg, err := loadRoot()
			if err != nil {
				return err
			}
			entries := lsEntries(cfg, reg, withGit)

			out := cmd.OutOrStdout()
			if asJSON {
				return ui.PrintJSON(out, entries)
			}
			if len(entries) == 0 {
				fmt.Fprintln(out, noWorkspaces)
				return nil
			}
			rows := make([][]string, 0, len(entries))
			for _, e := range entries {
				rows = append(rows, lsRow(e))
			}
			ui.Table(out, rows)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&withGit, "git", "g", false, "append per-project git branch and dirty state")
	return cmd
}

// lsEntries derives the full listing once, for both renderings — the human
// table and --json read the same values, so the two can never disagree.
// The git stats behind -g are fetched in a SINGLE bounded StatsFor call across
// every workspace's checked-out project dirs, rather than one call per
// workspace, so the worker bound holds globally and the walk is one wave.
func lsEntries(cfg *config.Config, reg alloc.Registry, withGit bool) []lsEntry {
	all := wsp.List(reg) // sorted by name; the output order for both renderings
	states := make([][]wsp.ProjectState, len(all))
	var dirs []string
	for i, ws := range all {
		states[i] = wsp.ProjectStates(cfg, ws)
		for _, st := range states[i] {
			dirs = append(dirs, st.Dir)
		}
	}
	var stats map[string]gitx.Stats
	if withGit {
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

// lsRow renders one entry as table cells: NAME  #INDEX  TASK_ID  DESCRIPTION,
// plus one PROJECT@BRANCH cell per checked-out project (with a '*' suffix when
// the tree is dirty) for `-g`. Exported package-privately because `status`
// without a workspace argument reuses this exact one-line format.
func lsRow(e lsEntry) []string {
	row := []string{e.Name, "#" + strconv.Itoa(e.Index), e.TaskID, e.Description}
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
	root, err := config.RootDir()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(root)
	if err != nil {
		return nil, nil, err
	}
	reg, err := alloc.Load(root)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: %w", err)
	}
	return cfg, reg, nil
}
