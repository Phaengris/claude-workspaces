package cli

import (
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/Phaengris/claude-workspaces/internal/alloc"
	"github.com/Phaengris/claude-workspaces/internal/ui"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
)

// portRange is one value's block for one workspace, inclusive on both ends.
type portRange struct {
	First int `json:"first"`
	Last  int `json:"last"`
}

// newPortsCmd builds `workspace ports`: the allocated value blocks across every
// workspace — value name × workspace, both sorted. The numbers come from
// alloc.Block (the same arithmetic that produces the runtime PORT0…PORTn), NOT
// from parsing alloc.ComputeValues' strings: a block is a first/last pair here,
// and re-deriving it from the rendered names would only add a way to be wrong.
func newPortsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ports",
		Short: "Show allocated value blocks across workspaces",
		Args:  usageArgs(cobra.NoArgs), // extra args are a usage error → exit 2 (spec §9)
		RunE: func(cmd *cobra.Command, args []string) error {
			asJSON, err := cmd.Flags().GetBool("json")
			if err != nil {
				return err
			}
			cfg, reg, err := loadRoot()
			if err != nil {
				return err
			}
			all := wsp.List(reg) // sorted by name
			valueNames := slices.Sorted(maps.Keys(cfg.Values))

			out := cmd.OutOrStdout()
			if asJSON {
				// Nested maps: value → workspace name → block. encoding/json
				// sorts map keys, so the object order matches the table's.
				byValue := make(map[string]map[string]portRange, len(valueNames))
				for _, name := range valueNames {
					if len(all) == 0 {
						continue // no workspaces → no blocks; omit the value entirely
					}
					blocks := make(map[string]portRange, len(all))
					for _, ws := range all {
						first, last := alloc.Block(cfg.Values[name], ws.Alloc.Index)
						blocks[ws.Name()] = portRange{First: first, Last: last}
					}
					byValue[name] = blocks
				}
				return ui.PrintJSON(out, byValue)
			}
			if len(all) == 0 {
				fmt.Fprintln(out, noWorkspaces)
				return nil
			}
			rows := make([][]string, 0, len(valueNames)*len(all))
			for _, name := range valueNames {
				for _, ws := range all {
					first, last := alloc.Block(cfg.Values[name], ws.Alloc.Index)
					rows = append(rows, []string{
						name,
						ws.Name(),
						strconv.Itoa(first) + "-" + strconv.Itoa(last),
					})
				}
			}
			ui.Table(out, rows)
			return nil
		},
	}
}
