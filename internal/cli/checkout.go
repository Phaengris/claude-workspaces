package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Phaengris/claude-workspaces/internal/config"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
	"github.com/Phaengris/claude-workspaces/internal/xerr"
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
			return checkoutWork(cfg, ws, args[1:])
		},
	}
}

// checkoutWork is checkout's body: resolve the named projects, run the
// ensure-chain for each in dependency order (join-and-continue), refresh
// WORKSPACE.md regardless. Shared with `launch`, whose reuse path checks extra
// projects out into an existing workspace — exactly this operation, and it must
// not fork into a second implementation.
func checkoutWork(cfg *config.Config, ws wsp.Workspace, names []string) error {
	ordered, err := resolveProjectNames(cfg, names)
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range ordered {
		if err := wsp.EnsureProject(cfg, ws, name, nil); err != nil {
			errs = append(errs, err) // already prefixed `project "<name>": …`
		}
	}
	if err := wsp.WriteWorkspaceMD(cfg, ws); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...) // nil when everything succeeded
}

// resolveProjectNames validates the named projects before ANY of them is acted
// on, deduplicates (`checkout a a` must not do the work twice) and returns them
// in dependency order. An unconfigured name is exit 3 with env/cd's message:
// that code covers the project half of an identifier exactly as it covers an
// unknown workspace. Shared by checkout, new and launch — the "nothing happens
// until every name is known" promise is the same in all three.
func resolveProjectNames(cfg *config.Config, names []string) ([]string, error) {
	seen := make(map[string]bool, len(names))
	uniq := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := cfg.Projects[name]; !ok {
			return nil, xerr.Wrap(xerr.ErrNotFound, fmt.Errorf("project %q is not configured", name))
		}
		if !seen[name] {
			seen[name] = true
			uniq = append(uniq, name)
		}
	}
	return wsp.TopoOrder(cfg, uniq)
}
