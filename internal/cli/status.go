package cli

import (
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/spf13/cobra"

	"github.com/Phaengris/claude-workspaces/internal/config"
	"github.com/Phaengris/claude-workspaces/internal/ui"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
)

// taskSep joins a task id to its description on the `task:` line. It is an
// em-dash, not a hyphen, so it never collides with the hyphens task ids
// themselves carry (A-1, PROJ-1234) — a reader can always tell where the id
// ends. When the description is empty the separator is omitted entirely rather
// than left dangling (see statusTaskLine).
const taskSep = " — "

// statusProject is one CONFIGURED project as `status <ws>` reports it. Unlike
// lsProject (which only ever describes checked-out projects) this covers the
// whole configured set, so CheckedOut is load-bearing: a false value means the
// remaining fields are zero because there is nothing in this workspace to
// measure, not because git failed.
// Daemons is a POINTER to a slice for the same reason lsEntry.Projects is: it
// distinguishes "measured, none configured" (empty array) from "not measured"
// (absent). Only a CHECKED-OUT project is measured — a project that is not
// here has no processes to have, and the existing one-line convention for it
// stays one line.
type statusProject struct {
	Name         string          `json:"name"`
	Dir          string          `json:"dir"`
	CheckedOut   bool            `json:"checked_out"`
	Branch       string          `json:"branch"`
	SetupCurrent bool            `json:"setup_current"`
	Daemons      *[]statusDaemon `json:"daemons,omitempty"`
}

// statusDaemon is one configured daemon's live state under its project. Name
// is the DAEMON name, not the `project:daemon` key: the project is the
// enclosing section in both renderings, so repeating it on every line (and in
// every JSON object) would be noise. Pid is 0 whenever Running is false —
// wsp.DaemonState collapses every not-running reason to that, and the human
// line omits it entirely (see statusDaemonDetail).
type statusDaemon struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Pid     int    `json:"pid"`
}

// statusEntry is the full derived view of one workspace: the registry's own
// fields (flattened, exactly as lsEntry presents them so the two renderings
// agree) plus every configured project's state. Projects is a plain slice, not
// a pointer like lsEntry.Projects — `status <ws>` always measures, so the key
// is always present, empty array included.
type statusEntry struct {
	Dir         string          `json:"dir"`
	Name        string          `json:"name"`
	Index       int             `json:"index"`
	TaskID      string          `json:"task_id"`
	Description string          `json:"description"`
	CreatedAt   string          `json:"created_at"`
	Adopted     bool            `json:"adopted"`
	Projects    []statusProject `json:"projects"`
}

// newStatusCmd builds `workspace status [workspace]`. With an identifier it
// prints one workspace in full (spec §2: every field derived live from the
// registry, the filesystem and git — nothing about a workspace is stored
// beyond its allocation). Without one it degenerates to the `ls` listing, via
// the same row builder, so the two commands can never drift apart.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [workspace]",
		Short: "Show one workspace in detail, or list all workspaces",
		// At most one identifier; a second positional is a usage error → exit 2
		// (spec §9).
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := cmd.Flags().GetBool("json")
			if err != nil {
				return err
			}
			cfg, reg, err := loadRoot()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			if len(args) == 0 {
				// The listing must stay as cheap as `ls`: withGit=false, because
				// wsp.ProjectStates is a git caller and the one-line format has
				// nowhere to show a branch anyway (see lsEntries).
				entries := lsEntries(cfg, reg, false)
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
			}

			ws, err := wsp.Resolve(reg, args[0]) // ErrNotFound → exit 3
			if err != nil {
				return err
			}
			entry := statusOf(cfg, ws)
			if asJSON {
				return ui.PrintJSON(out, entry)
			}
			printStatus(out, entry)
			return nil
		},
	}
}

// statusOf derives one workspace's full state. The project list covers EVERY
// configured project, sorted: wsp.ProjectStates reports only what is actually
// checked out here (Task 4's ratified reading), so the rest are filled in from
// cfg.Projects as "not checked out". A workspace is a subset of the configured
// projects, and `status` is the one place that subset should be visible — a
// reader asking "why is lib missing?" gets an answer instead of silence.
func statusOf(cfg *config.Config, ws wsp.Workspace) statusEntry {
	states := make(map[string]wsp.ProjectState, len(cfg.Projects))
	for _, st := range wsp.ProjectStates(cfg, ws) {
		states[st.Name] = st
	}
	projects := make([]statusProject, 0, len(cfg.Projects))
	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		if st, ok := states[name]; ok {
			daemons := statusDaemonsOf(cfg, ws, name)
			projects = append(projects, statusProject{
				Name:         st.Name,
				Dir:          st.Dir,
				CheckedOut:   true,
				Branch:       st.Branch,
				SetupCurrent: st.SetupCurrent,
				Daemons:      &daemons,
			})
			continue
		}
		// Not checked out: report where it WOULD live (so a caller can act on
		// the path) and leave branch/setup zero — there is nothing to measure.
		projects = append(projects, statusProject{Name: name, Dir: wsp.ProjectDir(ws, cfg, name)})
	}
	return statusEntry{
		Dir:         ws.Dir,
		Name:        ws.Name(),
		Index:       ws.Alloc.Index,
		TaskID:      ws.Alloc.TaskID,
		Description: ws.Alloc.Description,
		CreatedAt:   ws.Alloc.CreatedAt,
		Adopted:     ws.Alloc.Adopted,
		Projects:    projects,
	}
}

// statusDaemonsOf measures one checked-out project's configured daemons, in
// LISTED order — start order (spec §7), the sequence `up` follows — rather than
// sorted: what a reader wants from this block is the stack as it comes up. The
// result is never nil (an empty, non-nil slice for a project that configures no
// daemons), which is what makes the JSON key present-but-empty; see
// statusProject.Daemons.
func statusDaemonsOf(cfg *config.Config, ws wsp.Workspace, project string) []statusDaemon {
	ds := wsp.DaemonsOf(cfg, project)
	out := make([]statusDaemon, 0, len(ds))
	for _, d := range ds {
		running, pid := wsp.DaemonState(ws, d)
		out = append(out, statusDaemon{Name: d.Name, Running: running, Pid: pid})
	}
	return out
}

// printStatus renders the human block. Deliberately NOT ui.Table: the header is
// a fixed set of `key: value` lines, and column-aligning them against each
// other (or against the project lines) would make the whole block shift shape
// as a description grows. The exact lines are a contract
// (testdata/status_env.txtar).
//
// The human block shows what a person acts on — name, task, dir, index,
// projects. created_at and adopted are --json only: they are registry
// bookkeeping, and `ls` already surfaces them for scanning.
//
// A root with no configured projects prints "projects: none" on one line rather
// than a bare "projects:" header with nothing under it, matching how doctor
// reports an empty project set.
func printStatus(w io.Writer, entry statusEntry) {
	fmt.Fprintln(w, "workspace:", entry.Name)
	fmt.Fprintln(w, "task:", statusTaskLine(entry.TaskID, entry.Description))
	fmt.Fprintln(w, "dir:", entry.Dir)
	fmt.Fprintln(w, "index:", entry.Index)
	if len(entry.Projects) == 0 {
		fmt.Fprintln(w, "projects: none")
		return
	}
	fmt.Fprintln(w, "projects:")
	for _, p := range entry.Projects {
		fmt.Fprintf(w, "  %s: %s\n", p.Name, statusProjectDetail(p))
		// Daemons hang off their project line at one extra indent level. Nil
		// (not checked out) prints nothing at all — see statusProject.Daemons.
		if p.Daemons == nil {
			continue
		}
		for _, d := range *p.Daemons {
			fmt.Fprintf(w, "    %s: %s\n", d.Name, statusDaemonDetail(d))
		}
	}
}

// statusTaskLine renders the `task:` value: the task id, then the description
// after an em-dash. An empty description yields the bare id — a trailing
// " — " would read as a truncated line, and the description is genuinely
// optional (adopted workspaces can carry none).
func statusTaskLine(taskID, description string) string {
	if description == "" {
		return taskID
	}
	return taskID + taskSep + description
}

// statusProjectDetail renders what follows "  <name>: " on a project line.
//
// Two decided edges, both pinned by txtar:
//   - Branch "" means the work tree exists but its branch could not be read
//     (unborn HEAD in a fresh repo, or a git failure) → "branch unknown". It is
//     NOT rendered as an empty parenthetical, which would look like a bug.
//   - SetupCurrent false covers both a missing stamp and a mismatched one, and
//     both print "setup stale". The distinction does not change what a user does
//     next (M2's `up` re-runs setup either way), and inventing a third word for
//     "never set up" would imply an action that does not exist.
func statusProjectDetail(p statusProject) string {
	if !p.CheckedOut {
		return "not checked out"
	}
	branch := p.Branch
	if branch == "" {
		branch = "unknown"
	}
	setup := "stale"
	if p.SetupCurrent {
		setup = "current"
	}
	return fmt.Sprintf("checked out (branch %s), setup %s", branch, setup)
}

// statusDaemonDetail renders what follows "    <name>: " on a daemon line.
// A running daemon carries the pid, which is the one thing a user acts on
// (attach, inspect, kill by hand); a stopped one carries nothing — its pid is
// 0 for every not-running reason (missing, stale or recycled record — see
// wsp.DaemonState), and "(pid 0)" would read as a real process.
func statusDaemonDetail(d statusDaemon) string {
	if !d.Running {
		return "stopped"
	}
	return fmt.Sprintf("running (pid %d)", d.Pid)
}
