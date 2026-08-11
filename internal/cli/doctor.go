package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Phaengris/claude-workspaces/internal/alloc"
	"github.com/Phaengris/claude-workspaces/internal/config"
	"github.com/Phaengris/claude-workspaces/internal/proc"
	"github.com/Phaengris/claude-workspaces/internal/ui"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
)

// Observation kinds — the machine handle a --json consumer branches on, and
// therefore as much a contract as the human lines (pinned in doctor_test.go and
// doctor_findings.txtar). Snake_case, matching every other JSON field the tool
// emits.
//
// kindAllocationOutsideRoot is deliberately ONE kind for both the adopted and
// the not-adopted case: the situation is identical, only its legitimacy differs,
// and which array the entry lands in already says that. A consumer that wants
// the distinction reads the array, not a second kind string.
const (
	kindStaleAllocation       = "stale_allocation"
	kindAllocationOutsideRoot = "allocation_outside_root"
	kindAllocationUnreadable  = "allocation_unreadable"
	kindProjectRepoMissing    = "project_repo_missing"
	kindStalePidFile          = "stale_pid_file"
	kindPidsUnreadable        = "pids_unreadable"
	kindDaemonsRunning        = "daemons_running"
)

// gcHint closes every finding `workspace gc` is the fix for. doctor reports and
// gc/down fix (the decided doctor row), so the actionable findings all end with
// the SAME trailing hint — the position is invariant across classes, which is
// what makes the human output greppable and keeps a reader from having to know
// which command handles which class.
const gcHint = "(run: workspace gc)"

// doctorEntry is one observation as --json renders it.
//
// Detail is the human line VERBATIM — the same string doctor printed. That is
// deliberate: the line is built once and used for both renderings, so the two
// can never drift, and a consumer that only wants to show the problem needs no
// formatting of its own. Kind is what a consumer branches on; Workspace is the
// workspace NAME (never the dir — the dir, when it matters, is in Detail) and is
// absent for config-wide observations, which belong to no workspace.
type doctorEntry struct {
	Kind      string `json:"kind"`
	Workspace string `json:"workspace,omitempty"`
	Detail    string `json:"detail"`
}

// doctorReport is the whole `doctor --json` document: the config verdict plus
// the observations, SPLIT into the ones that are problems and the ones that are
// merely worth knowing. Both arrays are always present and never null (a nil
// slice would marshal to null and force every consumer to special-case it), so
// `findings: []` is the machine-readable "healthy".
//
// Config is the string "ok" and nothing else today: a config that is NOT ok
// never reaches this struct — Load's error is returned as-is (exit 4), which is
// the pre-existing contract. The field exists so a consumer can assert the
// section was reached rather than inferring it from an empty findings array.
type doctorReport struct {
	Config        string        `json:"config"`
	Findings      []doctorEntry `json:"findings"`
	Informational []doctorEntry `json:"informational"`
}

// doctorObs is one observation in REPORTING order, before it is split into the
// report's two arrays. Keeping a single ordered slice (rather than appending
// into two) is what lets the human output stay in section order — allocations,
// projects, daemons — with each workspace's informational and finding lines
// where the reader expects them, next to each other.
//
// note marks an observation that is NOT a finding: reported, but not counted by
// the summary and not a reason for anyone to act.
type doctorObs struct {
	kind      string
	workspace string
	detail    string
	note      bool
}

// newDoctorCmd builds `workspace doctor`: the read-only health report for a
// whole root (spec §2). It CHECKS and it never fixes — `gc` collects stale
// records, `down` stops daemons — so findings are not failures: the command
// exits 0 no matter how many it prints. Only the two pre-existing failure modes
// keep non-zero codes: an invalid or missing config is Load's ErrConfig (exit 4,
// message listing every problem at once) and an unreadable registry is a plain
// error (exit 1). Extra positionals are a usage error (exit 2).
//
// Sections, in output order:
//
//  1. config — `config: OK`, the configured project list, and each value block.
//     doctor is the one command that legitimately enumerates the whole config;
//     everything else names only what the user asked about.
//  2. allocations — per registry entry, in workspace-name order: a dir that no
//     longer exists is a stale allocation (gc's pass 1 is the fix); a dir
//     outside the root is reported LOUDLY either way, because it is normal for
//     an ADOPTED workspace (informational) and evidence of a corrupt or
//     hand-edited registry for a tool-created one (a finding — destroy already
//     refuses such an entry, and this is where a user finds out before they hit
//     that refusal).
//  3. projects — a configured project whose `repo` does not exist. Checked as
//     configured (tilde already expanded by Load, relative paths as written,
//     like every other repo consumer): the value is what checkout will hand to
//     git, so the value is what doctor stats. This is the classic
//     moved-a-machine failure, and the fix lives in config.yml, not in a
//     command.
//  4. daemons — per surviving workspace, by ENUMERATING wsp.PidsDir rather than
//     the config, for the reason gc's reap pass documents at length: a pid file
//     is named after the key `up` wrote it under, so a daemon renamed, dropped
//     from `start:`, or whose project left the config leaves a record no
//     config-driven walk can see — and that record is exactly what a health
//     report must not miss. Live records are counted (informational: running
//     daemons are the healthy state); dead or corrupt ones are stale pid files
//     (gc's pass 2 is the fix, and "corrupt reads as dead" is the same decided
//     Liveness row gc applies). A key the config cannot name is noted, never
//     hidden: for a dead record as a suffix on its line, for live ones as a
//     count on the workspace's note — a live daemon under an unlisted key cannot
//     be addressed by `workspace down <key>`, which is the one thing about it a
//     user needs to know.
//
// Summary: `doctor: N finding(s)` counting the findings only, or `doctor: no
// findings` when there are none. `--json` replaces the whole human rendering
// with doctorReport (no summary line: the array length is the count).
func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check config, registry, allocations, project repos and daemon health",
		Args:  usageArgs(cobra.NoArgs), // extra args are a usage error → exit 2 (spec §9)
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := cmd.Flags().GetBool("json")
			if err != nil {
				return err
			}
			root, err := config.RootDir()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err // ErrConfig → exit 4, message lists every problem
			}
			reg, err := alloc.Load(root)
			if err != nil {
				return fmt.Errorf("registry: %w", err)
			}
			obs := doctorObserve(root, cfg, reg)
			out := cmd.OutOrStdout()
			if asJSON {
				return ui.PrintJSON(out, doctorReportOf(obs))
			}
			printDoctorConfig(out, cfg)
			for _, o := range obs {
				fmt.Fprintln(out, o.detail)
			}
			printDoctorSummary(out, obs)
			return nil
		},
	}
}

// doctorObserve runs every check over the root and returns the observations in
// reporting order (see newDoctorCmd's section list). It reads only — nothing
// here mutates the registry, the filesystem, or any process.
func doctorObserve(root string, cfg *config.Config, reg alloc.Registry) []doctorObs {
	var obs []doctorObs
	find := func(kind, ws, detail string) {
		obs = append(obs, doctorObs{kind: kind, workspace: ws, detail: detail})
	}
	note := func(kind, ws, detail string) {
		obs = append(obs, doctorObs{kind: kind, workspace: ws, detail: detail, note: true})
	}

	// Section 2 — allocations. wsp.List sorts by name, which is what makes the
	// whole report deterministic.
	var live []wsp.Workspace
	for _, ws := range wsp.List(reg) {
		_, statErr := os.Stat(ws.Dir)
		switch {
		case errors.Is(statErr, fs.ErrNotExist):
			// Stale, and nothing else about it is worth checking: a dir that is
			// gone has no location to report and no pid files to read.
			find(kindStaleAllocation, ws.Name(), fmt.Sprintf("stale allocation: %s %s", ws.Dir, gcHint))
			continue
		case statErr != nil:
			// Unreadable is NOT vanished (gc's rule, and the reason it refuses to
			// release on doubt): report the uncertainty and check nothing further
			// here rather than guess in either direction.
			find(kindAllocationUnreadable, ws.Name(), fmt.Sprintf("allocation unreadable: %s: %v", ws.Dir, statErr))
			continue
		}
		live = append(live, ws)
		if outsideRoot(root, ws.Dir) {
			if ws.Alloc.Adopted {
				note(kindAllocationOutsideRoot, ws.Name(),
					fmt.Sprintf("allocation outside root: %s (adopted)", ws.Dir))
			} else {
				find(kindAllocationOutsideRoot, ws.Name(),
					fmt.Sprintf("allocation outside root: %s (NOT adopted — possibly corrupt registry)", ws.Dir))
			}
		}
	}

	// Section 3 — configured projects whose repo is not there. Only
	// fs.ErrNotExist counts: any other stat failure is not proof of absence,
	// the same asymmetry the allocation loop above applies.
	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		p := cfg.Projects[name]
		if p == nil {
			continue // validate already rejected this; nothing to stat
		}
		if _, err := os.Stat(p.Repo); errors.Is(err, fs.ErrNotExist) {
			find(kindProjectRepoMissing, "", fmt.Sprintf("project %q: repo does not exist: %s", name, p.Repo))
		}
	}

	// Section 4 — daemon health, from the pids directory of every workspace
	// whose dir is actually there.
	configured := configuredDaemonKeys(cfg)
	for _, ws := range live {
		keys, err := wsp.PidFileKeys(ws)
		if err != nil {
			// "Cannot tell" is a finding, never folded into "nothing runs here":
			// an unreadable pids dir hides exactly the records this section exists
			// to surface.
			find(kindPidsUnreadable, ws.Name(), fmt.Sprintf("pids dir unreadable: %s: %v", wsp.PidsDir(ws), err))
			continue
		}
		var running, unlisted int
		for _, key := range keys {
			pid, starttime, err := proc.ReadPidFile(filepath.Join(wsp.PidsDir(ws), key))
			switch {
			case err == nil && proc.Alive(pid, starttime):
				running++
				if !configured[key] {
					unlisted++
				}
			case errors.Is(err, fs.ErrNotExist):
				// Vanished while we looked: there is nothing left to report.
			default:
				// Dead or corrupt — both are stale records (the decided Liveness
				// row). The not-in-config note qualifies the key; the gc hint
				// stays last, where it is on every other actionable finding.
				detail := fmt.Sprintf("stale pid file: %s/%s", ws.Name(), key)
				if !configured[key] {
					detail += " (not in config)"
				}
				find(kindStalePidFile, ws.Name(), detail+" "+gcHint)
			}
		}
		if running == 0 {
			continue // nothing live here: nothing to note
		}
		detail := fmt.Sprintf("daemons: %s: %d running", ws.Name(), running)
		if unlisted > 0 {
			detail += fmt.Sprintf(" (%d not in config)", unlisted)
		}
		note(kindDaemonsRunning, ws.Name(), detail)
	}
	return obs
}

// outsideRoot reports whether dir is provably NOT strictly inside the
// workspaces root — the same component-wise containment question destroy asks
// before it removes anything (strictlyInside), so doctor's warning and destroy's
// refusal can never disagree.
//
// Both sides go through filepath.Abs first: config.RootDir comes from the
// environment and may be relative, and isAncestorOrSame treats mixed
// relative/absolute inputs as incomparable. An Abs failure (a broken cwd) means
// the question cannot be answered, and the answer is then FALSE — doctor states
// facts, and "outside the root" is an accusation of registry corruption that
// needs proof.
func outsideRoot(root, dir string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	return !strictlyInside(rootAbs, dirAbs)
}

// configuredDaemonKeys is the set of `project:daemon` keys the config currently
// names — what a pid file's name is checked AGAINST, never the source of the
// inventory (that is the pids directory; see newDoctorCmd's daemon section).
func configuredDaemonKeys(cfg *config.Config) map[string]bool {
	keys := make(map[string]bool)
	for name := range cfg.Projects {
		for _, d := range wsp.DaemonsOf(cfg, name) {
			keys[d.Key()] = true
		}
	}
	return keys
}

// doctorReportOf splits the ordered observations into the --json document,
// preserving their order within each array. Both slices start non-nil so a
// clean root marshals `[]` rather than `null` (see doctorReport).
func doctorReportOf(obs []doctorObs) doctorReport {
	rep := doctorReport{
		Config:        "ok",
		Findings:      []doctorEntry{},
		Informational: []doctorEntry{},
	}
	for _, o := range obs {
		e := doctorEntry{Kind: o.kind, Workspace: o.workspace, Detail: o.detail}
		if o.note {
			rep.Informational = append(rep.Informational, e)
			continue
		}
		rep.Findings = append(rep.Findings, e)
	}
	return rep
}

// printDoctorConfig writes the config section: the verdict, the configured
// project list, and one line per value block. Reaching this function at all
// means the config loaded and validated, hence the unconditional OK.
func printDoctorConfig(w io.Writer, cfg *config.Config) {
	fmt.Fprintln(w, "config: OK")
	names := slices.Sorted(maps.Keys(cfg.Projects))
	fmt.Fprintf(w, "projects (%d): %s\n", len(names), joinOr(names, "none"))
	valNames := make([]string, 0, len(cfg.Values))
	for name := range cfg.Values {
		valNames = append(valNames, name)
	}
	sort.Strings(valNames)
	for _, name := range valNames {
		v := cfg.Values[name]
		fmt.Fprintf(w, "values: %s = %d per workspace from %d\n", name, v.PerWorkspace, v.Start)
	}
}

// printDoctorSummary writes the last line: how many FINDINGS there were —
// informational observations are reported but never counted, so a root whose
// only lines are notes still reports a clean bill of health.
func printDoctorSummary(w io.Writer, obs []doctorObs) {
	n := 0
	for _, o := range obs {
		if !o.note {
			n++
		}
	}
	if n == 0 {
		fmt.Fprintln(w, "doctor: no findings")
		return
	}
	fmt.Fprintf(w, "doctor: %d finding(s)\n", n)
}

// joinOr joins items with ", ", or returns empty when there are none —
// doctor prints "none" rather than a blank list.
func joinOr(items []string, empty string) string {
	if len(items) == 0 {
		return empty
	}
	return strings.Join(items, ", ")
}
