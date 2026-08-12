package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/Phaengris/claude-workspaces/internal/wsp"
)

// projectStepper renders the ensure chain's progress for one project (spec
// 2026-08-12 rows 2-4): the label line is printed WITHOUT a newline before
// the step runs, and the SAME reporter completes it — `ok (2.1s)` or
// `failed` — so piped output always ends up line-clean without any tty
// probing. Durations are %.1f seconds, always shown: `(0.0s)` is the honest
// answer for an instant step. Setup output itself stays captured in
// proc.Run; these lines NAME the commands, they do not stream them.
func projectStepper(out io.Writer, project string) wsp.Step {
	return func(label string) func(error) {
		fmt.Fprintf(out, "  %s: %s… ", project, label)
		start := time.Now()
		return func(err error) {
			if err != nil {
				fmt.Fprintln(out, "failed")
				return
			}
			fmt.Fprintf(out, "ok (%.1fs)\n", time.Since(start).Seconds())
		}
	}
}
