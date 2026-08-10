package wsp

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/proc"
	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// The daemon model (spec §2/§3): which long-running processes a project
// declares, where their bookkeeping files live, whether they are running, and
// which of them a command line's targets name. Nothing here starts, stops or
// records anything — proc owns the mechanism, cli owns the orchestration; this
// file is the vocabulary the two share.

// Daemon is one named `start:` entry of one project. It carries its owning
// project because everything downstream — file names, output lines, error
// messages — addresses a daemon as `project:daemon`, and a bare name is
// ambiguous the moment two projects use it.
type Daemon struct {
	Project string
	Name    string
	Cmd     string
}

// Key is the daemon's stable identity, `<project>:<daemon>` — used verbatim
// both as the base name of its pid/log files and as its display name in
// command output, so what the user reads is what they can type back as a
// target. Config validation rejects ':' inside project and daemon names, which
// is what keeps the key parseable in both directions.
func (d Daemon) Key() string { return d.Project + ":" + d.Name }

// DaemonsOf lists the project's named `start:` entries — its daemons — in
// listed order, which is start order (spec §7; `down` reverses it). Bare
// entries are run-and-waits, not daemons, and are skipped. An unconfigured
// project has no daemons rather than being an error: callers iterate the
// result either way.
func DaemonsOf(cfg *config.Config, project string) []Daemon {
	p := cfg.Projects[project]
	if p == nil {
		return nil
	}
	var out []Daemon
	for _, e := range p.Start {
		if e.Name == "" {
			continue
		}
		out = append(out, Daemon{Project: project, Name: e.Name, Cmd: e.Cmd})
	}
	return out
}

// RunAndWaits lists the project's bare `start:` entries — commands that run to
// completion on every `up` of the project, before its daemons, in listed order
// (spec §7). They have no name, so they are neither addressable as targets nor
// tracked as state; re-running them is the whole point.
func RunAndWaits(cfg *config.Config, project string) []string {
	p := cfg.Projects[project]
	if p == nil {
		return nil
	}
	var out []string
	for _, e := range p.Start {
		if e.Name == "" {
			out = append(out, e.Cmd)
		}
	}
	return out
}

// StopCommands lists the project's `stop:` entries — extra commands `down`
// runs AFTER the project's daemons are stopped, in listed order, and only when
// the WHOLE project was targeted (they are the project's epilogue, the mirror
// of RunAndWaits' prelude). Like RunAndWaits they are unnamed and untracked;
// running them every whole-project down is the point.
func StopCommands(cfg *config.Config, project string) []string {
	p := cfg.Projects[project]
	if p == nil {
		return nil
	}
	return p.Stop
}

// PidsDir is the directory holding one pid file per daemon that has ever run in
// this workspace, `<ws.Dir>/.workspace/pids`. It is exported because it is the
// only complete inventory of the workspace's daemon records: a pid file is named
// after the daemon's key at the time `up` wrote it, so a daemon RENAMED or
// removed from config leaves a record no amount of config-walking can find,
// while the process it names still holds this workspace's ports. Anything
// deciding "is something running here?" or "which records are stale?" must
// enumerate this directory rather than iterate the config (gc's reap pass and
// the shared daemon gate both do). It may not exist — a workspace that never
// ran `up` has no such directory, which is simply "no records".
func PidsDir(ws Workspace) string {
	return filepath.Join(ws.Dir, stampDirName, "pids")
}

// PidFileKeys lists the daemon keys RECORDED in this workspace: the file names
// directly inside PidsDir, in os.ReadDir's sorted order, which is what keeps
// every caller's output stable. A missing directory yields no keys and no error
// (a workspace that never ran `up`); any other read failure IS an error, and
// deliberately not folded into "empty" — a caller about to stop, destroy or
// release something must be able to refuse rather than assume quiet.
// Subdirectories are skipped: nothing writes any, and a directory is not a pid
// file.
//
// This is the inventory PidsDir's comment describes, and the reason it is here
// rather than beside any one command: `down`/`restart` (the no-target stop),
// `gc` (reap + the destroy gate), `release` and `doctor` all need the SAME
// answer, and every one of them is wrong if it asks the config instead.
func PidFileKeys(ws Workspace) ([]string, error) {
	entries, err := os.ReadDir(PidsDir(ws))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		keys = append(keys, e.Name())
	}
	return keys, nil
}

// PidPath is where the daemon's `<pid> <starttime>` record lives:
// `<ws.Dir>/.workspace/pids/<project>:<daemon>`. One file per daemon, and the
// ONLY thing recorded about it — a daemon exists iff this file names a live
// process (spec §3). Creating the parent directory is the caller's job (`up`).
func PidPath(ws Workspace, d Daemon) string {
	return filepath.Join(PidsDir(ws), d.Key())
}

// LogPath is the daemon's stdout log, `<ws.Dir>/.workspace/logs/<key>.log`.
// Truncated at each start (spec §3), so it holds the current run only.
func LogPath(ws Workspace, d Daemon) string {
	return filepath.Join(ws.Dir, stampDirName, "logs", d.Key()+".log")
}

// ErrLogPath is the daemon's stderr log, `<key>.err.log`, beside LogPath and
// truncated with it.
func ErrLogPath(ws Workspace, d Daemon) string {
	return filepath.Join(ws.Dir, stampDirName, "logs", d.Key()+".err.log")
}

// DaemonState reports whether the daemon is running and, if so, its pid. The
// answer is derived, never stored: read the pid file, then ask proc whether
// that exact `(pid, starttime)` process is still alive.
//
// Every failure mode collapses to (false, 0) — missing file, corrupt content,
// a pid that died, a pid recycled by an unrelated process. A stopped daemon
// has no pid worth reporting, and `up` is free to overwrite whatever stale
// record it finds (the decided Liveness row). Callers that need to distinguish
// "no record" from "stale record" can read the pid file themselves; nothing in
// M3 does.
func DaemonState(ws Workspace, d Daemon) (running bool, pid int) {
	p, starttime, err := proc.ReadPidFile(PidPath(ws, d))
	if err != nil {
		return false, 0
	}
	if !proc.Alive(p, starttime) {
		return false, 0
	}
	return true, p
}

// TargetWork is one project's share of a resolved target list: the unit
// `up`/`down`/`restart`/`logs` iterate over.
//
//   - Project is a CONFIGURED project name (resolution never invents one).
//   - WholeProject records HOW the project was named — as a project (true) or
//     only through individual daemons of it (false). It is the flag `up` reads
//     to decide whether to run the project's run-and-waits and `down` reads to
//     decide whether to run its `stop:` commands; both act on the whole project
//     only when the whole project was targeted.
//   - Daemons is always the concrete list to act on, in the project's listed
//     order: every daemon of the project when WholeProject, otherwise just the
//     named ones. Callers never need to re-query DaemonsOf.
//
// Note the ensure-chain is NOT conditional on WholeProject: `up` runs
// EnsureProject for every entry, because a single-daemon target still needs
// its project checked out (Global Constraints).
type TargetWork struct {
	Project      string
	WholeProject bool
	Daemons      []Daemon
}

// ResolveTargets turns a command line's targets into an ordered work list
// (spec §2's target grammar). Each argument is matched in this order:
//
//  1. `project:daemon` — an explicit pair, always accepted when both halves
//     exist. An unknown project or a project without that daemon is
//     xerr.ErrNotFound naming the missing half (exit 3); a malformed pair
//     (empty half, extra colon) is xerr.ErrUsage (exit 2) — the argument is
//     not a name that failed to match, it is not a name at all.
//  2. a configured PROJECT name — the whole project. This rule is checked
//     before bare daemon names, so a project always wins the ambiguity with a
//     daemon of the same name; that daemon stays addressable as
//     `project:daemon`.
//  3. a bare DAEMON name unique across all CONFIGURED projects — that daemon,
//     with its owning project implied. Configured, not checked-out:
//     `up <ws> rails` from cold must resolve before any worktree exists, and
//     `up` creates the worktree itself. A name used by several projects is a
//     plain error (exit 1, not "not found") listing the `project:daemon`
//     candidates sorted — the name DID match, and guessing which project the
//     user meant could act on the wrong daemon. Nothing matching at all is
//     xerr.ErrNotFound.
//
// The first unresolvable argument stops resolution: acting on half a target
// list is worse than acting on none, and every command here mutates processes.
//
// No arguments means the whole workspace: every CHECKED-OUT project as a
// whole-project entry. Checked-out here, because "everything" can only mean
// what this workspace actually contains (ProjectStates' subset rule). A
// workspace with nothing checked out resolves to an EMPTY list and no error —
// there is genuinely nothing to do, and how loudly to say so is the caller's
// decision, not this function's.
//
// The result holds one entry per involved project, deduped (naming a project
// and one of its daemons yields a single whole-project entry — the superset
// wins) and ordered by TopoOrder over exactly the involved projects: `up`
// walks it forward, `down` backward.
func ResolveTargets(cfg *config.Config, ws Workspace, args []string) ([]TargetWork, error) {
	// acc accumulates per project while arguments are scanned; the concrete
	// daemon lists are materialized from config order at the end, which is
	// what makes repeats and orderings of the arguments irrelevant.
	type acc struct {
		whole  bool
		picked map[string]bool
	}
	entries := map[string]*acc{}
	touch := func(project string) *acc {
		a := entries[project]
		if a == nil {
			a = &acc{picked: map[string]bool{}}
			entries[project] = a
		}
		return a
	}

	if len(args) == 0 {
		for _, st := range ProjectStates(cfg, ws) {
			touch(st.Name).whole = true
		}
	}
	for _, arg := range args {
		switch {
		case strings.Contains(arg, ":"):
			project, name, err := splitTarget(arg)
			if err != nil {
				return nil, err
			}
			if cfg.Projects[project] == nil {
				return nil, xerr.Wrap(xerr.ErrNotFound, fmt.Errorf("target %q: no such project %q", arg, project))
			}
			if _, ok := daemonNamed(cfg, project, name); !ok {
				return nil, xerr.Wrap(xerr.ErrNotFound, fmt.Errorf("target %q: project %q has no daemon %q", arg, project, name))
			}
			touch(project).picked[name] = true
		case cfg.Projects[arg] != nil:
			touch(arg).whole = true
		default:
			matches := daemonsNamed(cfg, arg)
			switch len(matches) {
			case 1:
				touch(matches[0].Project).picked[matches[0].Name] = true
			case 0:
				return nil, xerr.Wrap(xerr.ErrNotFound, fmt.Errorf("no project or daemon named %q", arg))
			default:
				keys := make([]string, len(matches))
				for i, d := range matches {
					keys[i] = d.Key()
				}
				return nil, fmt.Errorf("daemon name %q is ambiguous: %s; use project:daemon",
					arg, strings.Join(keys, ", "))
			}
		}
	}

	order, err := TopoOrder(cfg, slices.Sorted(maps.Keys(entries)))
	if err != nil {
		return nil, err
	}
	out := make([]TargetWork, 0, len(order))
	for _, project := range order {
		a := entries[project]
		w := TargetWork{Project: project, WholeProject: a.whole}
		for _, d := range DaemonsOf(cfg, project) {
			if a.whole || a.picked[d.Name] {
				w.Daemons = append(w.Daemons, d)
			}
		}
		out = append(out, w)
	}
	return out, nil
}

// splitTarget parses an argument already known to contain ':' into its halves.
// Exactly one colon with both halves non-empty is the only accepted shape —
// names cannot contain ':' (config validation), so anything else is a typo
// rather than a name that might yet match something.
func splitTarget(arg string) (project, name string, err error) {
	project, name, _ = strings.Cut(arg, ":")
	if project == "" || name == "" || strings.Contains(name, ":") {
		return "", "", xerr.Wrap(xerr.ErrUsage, fmt.Errorf("invalid target %q: want project:daemon", arg))
	}
	return project, name, nil
}

// daemonNamed finds one project's daemon by name.
func daemonNamed(cfg *config.Config, project, name string) (Daemon, bool) {
	for _, d := range DaemonsOf(cfg, project) {
		if d.Name == name {
			return d, true
		}
	}
	return Daemon{}, false
}

// daemonsNamed finds every configured daemon with the given name, ordered by
// project name — the ambiguity error lists them, and that list must read the
// same on every run.
func daemonsNamed(cfg *config.Config, name string) []Daemon {
	var out []Daemon
	for _, project := range slices.Sorted(maps.Keys(cfg.Projects)) {
		if d, ok := daemonNamed(cfg, project, name); ok {
			out = append(out, d)
		}
	}
	return out
}
