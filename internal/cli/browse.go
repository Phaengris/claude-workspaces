package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Phaengris/claude-workspaces/internal/wsp"
	"github.com/Phaengris/claude-workspaces/internal/xerr"
)

// newBrowseCmd builds `workspace browse <workspace> [project]`: open the
// project's app — http://localhost:<port>, port = the project's browse_port
// after runtime substitution (so `${PORT0}` becomes THIS workspace's number).
//
// Project defaulting: exactly one checked-out project means it, unambiguously.
// Zero or several is a usage error — with several, guessing could open the
// wrong app, so the message lists the checked-out candidates to pick from. An
// EXPLICIT project only needs to be configured (exit 3 otherwise): the URL is
// derived from config + the allocation, and browsing something whose worktree
// happens to be elsewhere is still a meaningful ask.
//
// An empty browse_port (unset, or substituted to nothing) is a plain exit-1
// error naming the project: the identifier was fine, the config just has
// nothing to browse. Validation stays light on purpose — non-empty is the
// contract, numeric-ish is the user's business (spec leaves the value free).
//
// Opener: xdg-open when the process PATH has it — spawned DETACHED (started,
// released, never waited on; the CLI exits immediately and init reaps it) with
// the tool's own inherited env, not the curated one: xdg-open is the tool's
// helper needing the user's desktop session (DISPLAY, DBUS…), not user code
// under the spawn contract. Without xdg-open the URL is printed alone, exit 0
// — the SSH-friendly path, where printing IS the feature.
func newBrowseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "browse <workspace> [project]",
		Short: "Open the project's app in a browser (or print its URL)",
		// One or two positionals; anything else is a usage error → exit 2 (spec §9).
		Args: usageArgs(cobra.RangeArgs(1, 2)),
		// --json is inherited and deliberately unused: the plain URL line is
		// already the machine-readable form.
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, reg, err := loadRoot()
			if err != nil {
				return err
			}
			ws, err := wsp.Resolve(reg, args[0]) // ErrNotFound → exit 3
			if err != nil {
				return err
			}
			project := ""
			if len(args) == 2 {
				project = args[1]
				// Same rule and message as `env`: a typo'd project must fail
				// loudly, and only the CONFIG is consulted (see above).
				if _, ok := cfg.Projects[project]; !ok {
					return xerr.Wrap(xerr.ErrNotFound, fmt.Errorf("project %q is not configured", project))
				}
			} else {
				states := wsp.ProjectStates(cfg, ws)
				switch len(states) {
				case 1:
					project = states[0].Name
				case 0:
					return xerr.Wrap(xerr.ErrUsage, fmt.Errorf(
						"nothing checked out in %s; run: workspace checkout %s <project>", ws.Name(), ws.Name()))
				default:
					names := make([]string, len(states))
					for i, st := range states {
						names[i] = st.Name
					}
					return xerr.Wrap(xerr.ErrUsage, fmt.Errorf(
						"several projects are checked out; pick one: %s", strings.Join(names, ", ")))
				}
			}

			vars := wsp.RuntimeVars(cfg, ws.Alloc.TaskID, project, ws.Alloc.Index)
			port := strings.TrimSpace(wsp.Subst(cfg.Projects[project].BrowsePort, vars))
			if port == "" {
				return fmt.Errorf("project %q has no browse_port configured", project)
			}
			url := "http://localhost:" + port

			out := cmd.OutOrStdout()
			opener, err := exec.LookPath("xdg-open")
			if err != nil {
				fmt.Fprintln(out, url)
				return nil
			}
			open := exec.Command(opener, url)
			if err := open.Start(); err != nil {
				return err
			}
			_ = open.Process.Release() // detached: do not wait, do not track
			fmt.Fprintf(out, "opening %s\n", url)
			return nil
		},
	}
}
