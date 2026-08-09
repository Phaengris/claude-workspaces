package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/wsp"
	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// newCheckoutCmd builds `workspace checkout <workspace> <project…>`: the
// ensure-chain (worktree, .env, stamped setup) for each named project, in
// dependency order.
//
// Failure policy: every named project must resolve up front — an unconfigured
// name is exit 3 (same rule and message as env/cd) and nothing is ensured.
// Once ensuring starts, one project's failure does not abandon the rest:
// errors are collected, the remaining projects continue, and the joined error
// exits 1. Each ensure is idempotent, so re-running the same checkout
// converges on whatever failed. WORKSPACE.md is refreshed at the end
// REGARDLESS of failures — a half-succeeded checkout has changed what is
// checked out, and the file must describe reality, not the happy path.
func newCheckoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "checkout <workspace> <project> [project...]",
		Short: "Check projects out into a workspace (worktree, .env, setup)",
		// A workspace plus at least one project; fewer is a usage error →
		// exit 2 (spec §9).
		Args: usageArgs(cobra.MinimumNArgs(2)),
		// --json is inherited and deliberately unused: spec §2 scopes it to the
		// query commands. Accepting and ignoring it keeps `workspace --json
		// checkout X app` working for a caller that sets the flag globally,
		// rather than failing on a command with no query result to serialize.
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, reg, err := loadRoot()
			if err != nil {
				return err
			}
			ws, err := wsp.Resolve(reg, args[0]) // ErrNotFound → exit 3
			if err != nil {
				return err
			}

			// Validate every name before touching anything, deduplicating as
			// we go (checkout a a must not do the work twice). The message
			// matches env/cd: exit 3 covers the project half of an identifier
			// exactly as it covers an unknown workspace.
			seen := make(map[string]bool, len(args)-1)
			names := make([]string, 0, len(args)-1)
			for _, name := range args[1:] {
				if _, ok := cfg.Projects[name]; !ok {
					return xerr.Wrap(xerr.ErrNotFound, fmt.Errorf("project %q is not configured", name))
				}
				if !seen[name] {
					seen[name] = true
					names = append(names, name)
				}
			}

			ordered, err := wsp.TopoOrder(cfg, names)
			if err != nil {
				return err
			}

			var errs []error
			for _, name := range ordered {
				if err := wsp.EnsureProject(cfg, ws, name); err != nil {
					errs = append(errs, err) // already prefixed `project "<name>": …`
				}
			}
			if err := wsp.WriteWorkspaceMD(cfg, ws); err != nil {
				errs = append(errs, err)
			}
			return errors.Join(errs...) // nil when everything succeeded
		},
	}
}
