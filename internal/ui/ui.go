// Package ui renders command output. Every helper writes to a caller-supplied
// io.Writer — never to os.Stdout directly — so commands can pass
// cmd.OutOrStdout() while tests pass a bytes.Buffer and assert exact bytes.
// The package holds formatting only: no config, no filesystem, no git.
package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// PrintJSON writes v as two-space-indented JSON followed by a single trailing
// newline. The exact shape (json.MarshalIndent with "" prefix and "  " indent,
// plus "\n") is the contract every --json code path emits, so downstream
// consumers can diff output byte-for-byte; do not switch to json.Encoder, whose
// escaping and newline behavior differ. Marshal errors are returned unwrapped
// and nothing is written in that case.
func PrintJSON(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

// Table writes rows as space-aligned columns: minwidth 0, tabwidth 0, padding 2,
// space padding, no tabwriter flags — i.e. every column is exactly two spaces
// wider than its longest cell, and the last cell on a line gets no trailing
// padding. Headers are not special: a caller that wants them passes them as
// row 0. Cells must not contain tabs or newlines (they would break alignment).
// Rows of differing length are legal — tabwriter only aligns cells that are
// terminated by a tab, so a short final row simply ends early. Nothing is
// written for an empty rows slice.
func Table(w io.Writer, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	tw.Flush()
}
