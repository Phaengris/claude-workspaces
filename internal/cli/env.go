package cli

import (
	"fmt"
	"maps"
	"slices"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/ui"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// newEnvCmd builds `workspace env <workspace> [project]`: the environment a
// command would actually run with, as sorted K=V lines.
//
// The semantic this command exists to make visible (spec §4): runtime tokens —
// WORKSPACE, PROJECT, and the index-derived values (PORT0…) — are substitution
// INPUTS, not environment variables. They appear inside values (DB_NAME=app_A-1_dev)
// and never as keys of their own. What gets exported is exactly cfg.Env overlaid
// with the project's env, nothing more; a caller that wants the numbers
// themselves asks `workspace ports`.
//
// Project omitted → global env only, so ${PROJECT} has no value to substitute
// and passes through untouched (unknown tokens always do).
func newEnvCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "env <workspace> [project]",
		Short: "Show the resolved environment for a workspace (optionally one project)",
		// One or two positionals; anything else is a usage error → exit 2 (spec §9).
		Args: usageArgs(cobra.RangeArgs(1, 2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := cmd.Flags().GetBool("json")
			if err != nil {
				return err
			}
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
				// The command validates the name, because wsp.ResolvedEnv cannot:
				// an unknown project there silently yields the global env alone,
				// which prints as a plausible-looking result. A typo'd project
				// must fail loudly, and spec §9 governs the code: exit 3
				// ("workspace/project not found") covers this half of the
				// identifier exactly as it covers an unknown workspace.
				//
				// Only the CONFIG is consulted — a project need not be checked
				// out in this workspace for its env to resolve. The message names
				// the project and stops there: enumerating every configured
				// project turns a one-line error into a wall of unrelated names.
				if _, ok := cfg.Projects[project]; !ok {
					return xerr.Wrap(xerr.ErrNotFound, fmt.Errorf("project %q is not configured", project))
				}
			}

			env := wsp.ResolvedEnv(cfg, ws.Alloc.TaskID, project, ws.Alloc.Index)
			out := cmd.OutOrStdout()
			if asJSON {
				// A map: encoding/json sorts keys, so --json and the K=V lines
				// come out in the same order. An empty env is a valid `{}`.
				return ui.PrintJSON(out, env)
			}
			// No prose line for an empty env, unlike `ls`'s "no workspaces":
			// every line here is machine-consumable K=V, and a config with no
			// env legitimately produces zero of them. A "no env" line would be
			// something a caller has to filter out.
			for _, k := range slices.Sorted(maps.Keys(env)) {
				fmt.Fprintf(out, "%s=%s\n", k, env[k])
			}
			return nil
		},
	}
}
