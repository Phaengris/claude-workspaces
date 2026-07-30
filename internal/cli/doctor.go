package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
)

// newDoctorCmd builds `workspace doctor` (M0 scope): load + validate config,
// read the registry, flag allocations whose dir no longer exists. Config
// errors pass through unwrapped — Load already tags them ErrConfig (exit 4).
// Output lines are sorted and format-stable; the txtar scripts assert them
// verbatim. (Orphan-daemon and port checks arrive with M5's doctor full.)
func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Validate config.yml and check registry health",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := config.RootDir()
			if err != nil {
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				return err // ErrConfig → exit 4, message lists every problem
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "config: OK")

			names := make([]string, 0, len(cfg.Projects))
			for name := range cfg.Projects {
				names = append(names, name)
			}
			sort.Strings(names)
			fmt.Fprintf(out, "projects (%d): %s\n", len(names), joinOr(names, "none"))

			valNames := make([]string, 0, len(cfg.Values))
			for name := range cfg.Values {
				valNames = append(valNames, name)
			}
			sort.Strings(valNames)
			for _, name := range valNames {
				v := cfg.Values[name]
				fmt.Fprintf(out, "values: %s = %d per workspace from %d\n", name, v.PerWorkspace, v.Start)
			}

			reg, err := alloc.Load(root)
			if err != nil {
				return fmt.Errorf("registry: %w", err)
			}
			for _, dir := range sortedRegistryDirs(reg) {
				if _, err := os.Stat(dir); err != nil {
					fmt.Fprintf(out, "stale allocation: %s (dir missing — run `workspace gc`)\n", dir)
				}
			}
			return nil
		},
	}
}

// sortedRegistryDirs returns the registry's workspace dirs sorted, so doctor's
// stale-allocation lines have a deterministic order.
func sortedRegistryDirs(reg alloc.Registry) []string {
	dirs := make([]string, 0, len(reg))
	for dir := range reg {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

// joinOr joins items with ", ", or returns empty when there are none —
// doctor prints "none" rather than a blank list.
func joinOr(items []string, empty string) string {
	if len(items) == 0 {
		return empty
	}
	return strings.Join(items, ", ")
}
