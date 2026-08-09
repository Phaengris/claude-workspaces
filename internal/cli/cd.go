package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/wsp"
	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// newCdCmd builds `workspace cd <workspace> [project]`: the absolute directory
// of a workspace, or of one project inside it.
//
// WHY it only prints: a child process cannot change its parent shell's working
// directory — that is a property of the process model, not a missing feature. So
// the command's whole job is to resolve the path; the M5 shell function does the
// actual chdir (`cd "$(workspace cd "$1")"`). Naming it `cd` anyway is
// deliberate: the wrapper's name and the binary's subcommand match, so the two
// never have to be explained separately.
//
// The path is printed even when the directory does not exist yet (a configured
// project that is not checked out here). Answering "where it belongs" is what
// makes the output usable for M2's mutating commands; refusing to answer would
// force every caller to rebuild the path itself.
func newCdCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cd <workspace> [project]",
		Short: "Print the directory of a workspace, or of one project inside it",
		// One or two positionals; anything else is a usage error → exit 2 (spec §9).
		Args: usageArgs(cobra.RangeArgs(1, 2)),
		// --json is inherited and deliberately unused: the output is a single
		// absolute path, which is already the machine-readable form, and the
		// shell wrapper substitutes it verbatim. Accepting the flag and ignoring
		// it keeps `workspace --json cd X` (a caller that sets --json globally
		// for every call) working instead of failing on the one command where
		// JSON would only get in the way.
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, reg, err := loadRoot()
			if err != nil {
				return err
			}
			ws, err := wsp.Resolve(reg, args[0]) // ErrNotFound → exit 3
			if err != nil {
				return err
			}
			dir := ws.Dir
			if len(args) == 2 {
				project := args[1]
				// Validated against the CONFIG, exactly as `env` does: wsp.ProjectDir
				// falls back to the bare name for an unknown project, so a typo
				// would print a plausible path that nothing will ever create.
				// Exit 3 (not found), per spec §9: the project half of the
				// identifier didn't resolve, exactly as the workspace half would
				// not have. The message names the project and stops there rather
				// than listing every configured one.
				if _, ok := cfg.Projects[project]; !ok {
					return xerr.Wrap(xerr.ErrNotFound, fmt.Errorf("project %q is not configured", project))
				}
				dir = wsp.ProjectDir(ws, cfg, project)
			}
			fmt.Fprintln(cmd.OutOrStdout(), dir)
			return nil
		},
	}
}
