package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/Phaengris/chaban/v2/internal/wsp"
	"github.com/Phaengris/chaban/v2/internal/xerr"
)

// newChatCmd builds `chaban chat <workspace> [-R] [agent args...]`: start or
// resume the configured agent in the workspace dir.
//
// DisableFlagParsing: every flag except -R/--no-resume (scanned only before a
// literal `--`) belongs to the agent. The costs are handled by hand:
//   - no Args validator runs, so the missing-workspace usage error (exit 2) is
//     the explicit len check below;
//   - cobra never sees a help flag, so bare -h/--help in the workspace slot is
//     answered here before resolving;
//   - a flag-looking token BEFORE the workspace is a usage error, see
//     requireIdentFirst.
//
// Environment: the session gets the inherited env plus the workspace overlay
// (sessionEnv), not the curated env user commands get.
//
// Exit code: the agent's own code becomes ours, verbatim, via xerr.Exit.
func newChatCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "chat <workspace> [-R] [agent args...]",
		Short:              "Start or resume an agent session in the workspace",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			if err := requireIdentFirst("chat", "workspace", args); err != nil {
				return err
			}
			if len(args) == 0 {
				return xerr.Wrap(xerr.ErrUsage, errors.New("a workspace is required"))
			}
			noResume, rest, err := parseSessionFlags(args[1:])
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
			agent, err := resolveAgent(cfg, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			return runAgentSession(cfg, ws, agent, noResume, rest)
		},
	}
}

// newClaudeRemovedCmd keeps `chaban claude` as a hidden tombstone that points
// at its replacement.
func newClaudeRemovedCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "claude",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(*cobra.Command, []string) error {
			return xerr.Wrap(xerr.ErrUsage, errors.New("`chaban claude` is now `chaban chat`"))
		},
	}
}
