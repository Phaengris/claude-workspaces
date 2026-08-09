package wsp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/gitx"
	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// stampDirName holds the tool's per-workspace bookkeeping (spec §3). M1 only
// reads what lives there.
const stampDirName = ".workspace"

// Workspace pairs a workspace directory with its registry allocation. It is a
// value, not a handle: everything else (which projects are checked out, their
// branches, whether setup is current) is derived on demand from the filesystem
// and git rather than stored.
type Workspace struct {
	Dir   string
	Alloc alloc.Allocation
}

// Name is the workspace's full name — the base name of its directory, e.g.
// "T-1_add-widgets". The registry keys on the absolute dir, so the name is
// always derived, never a second source of truth.
func (w Workspace) Name() string {
	return filepath.Base(w.Dir)
}

// List returns every workspace in the registry, sorted by Name(). Map
// iteration order is random, so sorting here is what makes command output
// stable.
func List(reg alloc.Registry) []Workspace {
	out := make([]Workspace, 0, len(reg))
	for dir, a := range reg {
		out = append(out, Workspace{Dir: dir, Alloc: a})
	}
	slices.SortFunc(out, func(a, b Workspace) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return out
}

// Resolve finds the workspace an identifier names: first an exact full-name
// match, then an exact task-id match. Names win, so a workspace can always be
// addressed unambiguously by its own directory name even if some other
// workspace's task id happens to be the same string.
//
// A task id shared by several workspaces is a hard error listing their full
// names — deliberately NOT ErrNotFound, because the ident did match and
// picking one arbitrarily could act on the wrong workspace. Nothing matching
// at all is ErrNotFound (exit code 3).
func Resolve(reg alloc.Registry, ident string) (Workspace, error) {
	all := List(reg)
	for _, ws := range all {
		if ws.Name() == ident {
			return ws, nil
		}
	}
	var byTask []Workspace
	for _, ws := range all {
		if ws.Alloc.TaskID == ident {
			byTask = append(byTask, ws)
		}
	}
	switch len(byTask) {
	case 1:
		return byTask[0], nil
	case 0:
		return Workspace{}, xerr.Wrap(xerr.ErrNotFound, fmt.Errorf("no workspace matching %q", ident))
	default:
		names := make([]string, len(byTask))
		for i, ws := range byTask {
			names[i] = ws.Name()
		}
		return Workspace{}, fmt.Errorf("task id %q matches %d workspaces (%s); use a full name",
			ident, len(byTask), strings.Join(names, ", "))
	}
}

// ProjectDir is where the named project lives inside the workspace: the
// project's configured `path` when set, otherwise its config key. An
// unconfigured name falls back to the name itself, so callers can ask about a
// stray directory without a config lookup dance.
func ProjectDir(ws Workspace, cfg *config.Config, name string) string {
	sub := name
	if p := cfg.Projects[name]; p != nil && p.Path != "" {
		sub = p.Path
	}
	return filepath.Join(ws.Dir, sub)
}

// ProjectState is one project's derived state inside a workspace. CheckedOut
// is always true for states returned by ProjectStates; the field exists so
// consumers (ui, --json) can render it explicitly and so a future caller can
// build a state for an absent project.
type ProjectState struct {
	Name         string
	Dir          string
	Branch       string
	CheckedOut   bool
	SetupCurrent bool
}

// ProjectStates reports the configured projects that this workspace actually
// contains, sorted by name. The M1 containment rule is git's own answer:
// a project is checked out iff gitx.IsWorkTreeRoot(ProjectDir(...)). Projects
// that are configured but not checked out here are simply absent from the
// result — a workspace is a subset of the configured projects, and listing the
// rest as empty rows would misreport unrelated config as part of this
// workspace.
//
// ROOT, not merely "inside a work tree": if the workspaces area itself lives
// inside some enclosing repo, every plain directory under it is inside a work
// tree, and the looser predicate reported such a directory as checked out —
// with the ENCLOSING repo's branch. `status` then described a project that
// does not exist and `destroy` would have run its teardown commands there.
//
// Branch is best-effort: a work tree whose branch cannot be read (e.g. an
// unborn HEAD in a fresh repo) still yields a state, with Branch empty. A
// read-only view must degrade rather than fail.
func ProjectStates(cfg *config.Config, ws Workspace) []ProjectState {
	var out []ProjectState
	for _, name := range slices.Sorted(maps.Keys(cfg.Projects)) {
		dir := ProjectDir(ws, cfg, name)
		if !gitx.IsWorkTreeRoot(dir) {
			continue
		}
		branch, err := gitx.Branch(dir)
		if err != nil {
			branch = ""
		}
		out = append(out, ProjectState{
			Name:         name,
			Dir:          dir,
			Branch:       branch,
			CheckedOut:   true,
			SetupCurrent: setupCurrent(cfg, ws, name),
		})
	}
	return out
}

// SetupHash is the hex SHA-256 of the project's setup commands as they would
// actually run: each command rendered through Subst with this workspace's
// runtime vars, joined by newlines. Rendering before hashing is what makes the
// stamp meaningful — the same command text with a different port really is
// different work. A project with no setup (or no such project) hashes the
// empty string, which is stable and comparable like any other value.
func SetupHash(cfg *config.Config, ws Workspace, project string) string {
	var cmds []string
	if p := cfg.Projects[project]; p != nil {
		vars := RuntimeVars(cfg, ws.Alloc.TaskID, project, ws.Alloc.Index)
		cmds = make([]string, len(p.Setup))
		for i, c := range p.Setup {
			cmds[i] = Subst(c, vars)
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(cmds, "\n")))
	return hex.EncodeToString(sum[:])
}

// setupCurrent reports whether <ws.Dir>/.workspace/setup-<project>.ok records
// the current SetupHash. A missing or unreadable stamp is "not current", which
// is the safe direction: M2's `up` then re-runs setup, and re-running setup is
// expected to be idempotent (spec §3).
func setupCurrent(cfg *config.Config, ws Workspace, project string) bool {
	data, err := os.ReadFile(stampPath(ws, project))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == SetupHash(cfg, ws, project)
}

// stampPath is the setup stamp file for one project in one workspace.
func stampPath(ws Workspace, project string) string {
	return filepath.Join(ws.Dir, stampDirName, "setup-"+project+".ok")
}
