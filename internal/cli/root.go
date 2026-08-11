// Package cli builds the workspace command tree.
package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Phaengris/claude-workspaces/internal/envx"
	"github.com/Phaengris/claude-workspaces/internal/xerr"
)

// version is stamped at release time via -ldflags "-X …/internal/cli.version=v…".
var version = "dev"

// Root builds the command tree. A fresh tree per call keeps tests independent.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:           "workspace",
		Short:         "Isolated, runnable dev-stack instances for parallel Claude Code sessions",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// Flag-parse failures (unknown/bad flags) are usage errors → exit 2 (spec
	// §9). Set on the root only; cobra's FlagErrorFunc lookup walks to the parent,
	// so every subcommand inherits it.
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return xerr.Wrap(xerr.ErrUsage, err)
	})
	// --json is global on purpose: every query command honors it — M1's ls,
	// ports, status, env, which, cd and M5's doctor — so it is registered once
	// as a persistent flag
	// rather than per-command. Commands read it with cmd.Flags().GetBool("json")
	// — cobra's Flags() includes inherited persistent flags — and render via
	// internal/ui.PrintJSON. A command that grows its own local "json" flag
	// would shadow this one; don't.
	root.PersistentFlags().Bool("json", false, "machine-readable output")
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		envx.SanitizeSelf() // undo version-manager activation before anything spawns (spec §6)
	}
	// Use-string convention, applied by every command in the tree: <angle
	// brackets> for a required positional, [square brackets] for an optional one.
	// So `status [workspace]` takes it or leaves it, `env <workspace> [project]`
	// and `cd <workspace> [project]` require the workspace, and `ls`/`ports`/
	// `which`/`doctor` take none. cobra prints these verbatim in help and in
	// usage errors, so a drifting style is a user-visible inconsistency.
	root.AddCommand(newLsCmd())
	root.AddCommand(newPortsCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newEnvCmd())
	root.AddCommand(newWhichCmd())
	root.AddCommand(newCdCmd())
	root.AddCommand(newNewCmd())
	root.AddCommand(newCheckoutCmd())
	root.AddCommand(newUpCmd())
	root.AddCommand(newDownCmd())
	root.AddCommand(newRestartCmd())
	root.AddCommand(newLogsCmd())
	root.AddCommand(newExecCmd())
	root.AddCommand(newBrowseCmd())
	root.AddCommand(newAdoptCmd())
	root.AddCommand(newReleaseCmd())
	root.AddCommand(newDestroyCmd())
	root.AddCommand(newGCCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newClaudeCmd())
	root.AddCommand(newLaunchCmd())
	root.AddCommand(newInstallCmd())
	root.AddCommand(newUninstallCmd())
	// Dynamic shell completions, wired in one table after the tree exists (see
	// completion.go): every command gets a deliberate suggestion policy, and no
	// command falls back to completing file names in a workspace slot.
	wireCompletions(root)
	return root
}

// usageArgs wraps a positional-args validator so a violation (a leaf command's
// wrong arg count) carries xerr.ErrUsage → exit 2 (spec §9). This is the
// structural interception point for arg-count errors: cobra runs a command's
// Args validator in execute() and returns its plain error with no typed kind,
// so tagging it here beats string-matching the message. (Unknown-command
// errors take a different path — see classifyUsageError.)
func usageArgs(next cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := next(cmd, args); err != nil {
			return xerr.Wrap(xerr.ErrUsage, err)
		}
		return nil
	}
}

// Main runs the CLI and returns the process exit code. It is the single exit
// point: commands return errors (wrapped with an xerr kind where specific),
// nothing below main prints-and-dies.
func Main() int {
	err := classifyUsageError(Root().Execute())
	// A propagated child exit code (xerr.ExitError, from the claude session
	// runner) is deliberately NOT printed: the child owned the terminal and
	// said whatever it had to say; a trailing "workspace: exit status 7"
	// after every non-zero session would be pure noise. The code still
	// propagates via ExitCode below.
	var ee *xerr.ExitError
	if err != nil && !errors.As(err, &ee) {
		fmt.Fprintln(os.Stderr, "workspace:", err)
	}
	return xerr.ExitCode(err)
}

// classifyUsageError tags cobra's unknown-command error with xerr.ErrUsage so
// it exits 2 (spec §9). WHY string matching here: an unknown subcommand is
// reported by cobra's Command.Find via legacyArgs, before the target command's
// Args validator runs, as a plain error carrying no typed kind. Unlike flag
// errors (intercepted structurally by SetFlagErrorFunc) and arg-count errors
// (intercepted structurally by usageArgs on a command's Args), it has no
// structural hook: the root command is not Runnable, so cobra short-circuits to
// help/ErrHelp before ValidateArgs, and Find hardcodes legacyArgs when Args is
// nil. Matching cobra's stable "unknown command" prefix is the documented
// last-resort classification, kept in this one place. Errors already carrying
// an xerr kind (usage, config, not-found) pass through untouched.
func classifyUsageError(err error) error {
	if err == nil {
		return err
	}
	if errors.Is(err, xerr.ErrUsage) || errors.Is(err, xerr.ErrConfig) || errors.Is(err, xerr.ErrNotFound) {
		return err
	}
	if strings.HasPrefix(err.Error(), "unknown command") {
		return xerr.Wrap(xerr.ErrUsage, err)
	}
	return err
}
