// Package cli builds the workspace command tree.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/xerr"
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
	return root
}

// Main runs the CLI and returns the process exit code. It is the single exit
// point: commands return errors (wrapped with an xerr kind where specific),
// nothing below main prints-and-dies.
func Main() int {
	err := Root().Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, "workspace:", err)
	}
	return xerr.ExitCode(err)
}
