package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/ui"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// whichEntry is `which --json`: the located workspace's identity, nothing
// derived. A caller that wants project state follows up with `status`, which is
// the command that pays for git.
type whichEntry struct {
	Dir    string `json:"dir"`
	Name   string `json:"name"`
	TaskID string `json:"task_id"`
}

// newWhichCmd builds `workspace which`: the workspace containing the current
// directory, by name. It is the inverse of `cd` — the shell wrapper and the
// Claude skill use it to answer "where am I?" without the caller having to
// parse a path.
//
// Registry lookup only: a directory is inside a workspace iff a registry entry
// is that directory or one of its ancestors. There is no filesystem probing and
// no git — the registry is the single source of truth about what a workspace is
// (spec §3), so an unregistered directory that merely looks like a workspace is
// correctly reported as "not inside a workspace".
func newWhichCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "which",
		Short: "Print the workspace containing the current directory",
		Args:  usageArgs(cobra.NoArgs), // extra args are a usage error → exit 2 (spec §9)
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := cmd.Flags().GetBool("json")
			if err != nil {
				return err
			}
			// loadRoot, not alloc.Load alone: which needs no config value, but
			// every read-only command must fail the same way on a broken root
			// (config → exit 4, unreadable registry → exit 1). Diagnosing the
			// root twice differently would be worse than the wasted parse.
			_, reg, err := loadRoot()
			if err != nil {
				return err
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			ws, ok := workspaceAt(reg, cwd)
			if !ok {
				// ErrNotFound (exit 3), matching `status <unknown>`: a script can
				// use `workspace which` as the test for "am I in a workspace?"
				// and distinguish that from a broken root (4) or a bad flag (2).
				return xerr.Wrap(xerr.ErrNotFound, errors.New("not inside a workspace"))
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return ui.PrintJSON(out, whichEntry{
					Dir:    ws.Dir,
					Name:   ws.Name(),
					TaskID: ws.Alloc.TaskID,
				})
			}
			fmt.Fprintln(out, ws.Name())
			return nil
		},
	}
}

// workspaceAt finds the workspace whose dir contains path (or is path itself).
//
// The DEEPEST match wins. Workspaces can nest — nothing forbids a root inside
// another workspace's tree, and M2's adopt makes that reachable — and in that
// case the innermost enclosing workspace is the one the caller is working in.
// Among dirs that are all ancestors of the same path, the deepest is simply the
// longest (each is a prefix of the next in component terms), so string length
// is a sound comparison here even though string prefixes are NOT a sound
// ancestry test — see isAncestorOrSame.
//
// Symlinks are compared as written, not resolved: os.Getwd returns the logical
// cwd, and the M5 shell wrapper cds to the path `cd` printed (i.e. the registry's
// own spelling), so the two agree by construction. A caller that reached the
// same directory through a different symlinked route gets "not inside a
// workspace" — deliberately not papered over with filepath.EvalSymlinks, which
// would make the answer depend on filesystem state that the registry does not
// record. Revisit if a real workflow hits it.
func workspaceAt(reg alloc.Registry, path string) (wsp.Workspace, bool) {
	var (
		best  wsp.Workspace
		found bool
	)
	for _, ws := range wsp.List(reg) {
		if !isAncestorOrSame(ws.Dir, path) {
			continue
		}
		if !found || len(filepath.Clean(ws.Dir)) > len(filepath.Clean(best.Dir)) {
			best, found = ws, true
		}
	}
	return best, found
}

// allocationAt finds the workspace whose dir IS path — no ancestry walk. It is
// workspaceAt's exact-match sibling, for the commands that address a workspace
// by its own directory rather than by being somewhere inside it: `adopt`
// (is this dir already allocated?) and `release <dir>` (which allocation does
// this dir name?).
//
// Comparison is on filepath.Clean, not on the raw map key: a registry key is
// written by us and should already be clean and absolute, but a caller's path
// arrives from a command line and a hand-edited registry can hold anything, so
// "/ws/T-1/" and "/ws/T-1" must not name two different workspaces. Callers act
// on the returned ws.Dir — the registry's OWN spelling — so a release always
// deletes the key that exists.
//
// Like workspaceAt, symlinks are compared as written; see its note on why.
func allocationAt(reg alloc.Registry, path string) (wsp.Workspace, bool) {
	want := filepath.Clean(path)
	for _, ws := range wsp.List(reg) {
		if filepath.Clean(ws.Dir) == want {
			return ws, true
		}
	}
	return wsp.Workspace{}, false
}

// isAncestorOrSame reports whether dir is path or one of path's ancestors,
// comparing PATH COMPONENTS rather than characters.
//
// The bug this exists to prevent is the classic one: strings.HasPrefix says
// /ws/A-1 contains /ws/A-1b, because "A-1" is a character prefix of "A-1b". So
// `which` run inside the sibling workspace A-1_xtra would name A-1_x, and a
// caller acting on that answer would operate on the wrong workspace. Guarding
// with a trailing separator (HasPrefix(path, dir+"/")) fixes that one case but
// still mishandles "." and ".." segments and the equal-paths case, so the
// component-wise question is asked directly instead.
//
// filepath.Rel is that question: it Cleans both sides and returns the route
// from dir to path. "." means the same directory; a route that starts by going
// UP (".." as its first component) means dir is not an ancestor. An error means
// the two are not comparable at all (one relative, one absolute, or different
// Windows volumes) — not an ancestor either. The IsAbs check is belt-and-braces
// for that last family: Rel never returns an absolute path today, and a caller
// relying on it would be relying on an accident.
func isAncestorOrSame(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if filepath.IsAbs(rel) {
		return false
	}
	if rel == "." {
		return true // same directory: dir contains path trivially
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
