package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/wsp"
	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// newLaunchCmd builds `workspace launch <task_id> [<description> [project…]]
// [-S] [-R] [-- claude args…]`: the one-shot that composes the whole daily
// entry sequence — create-or-reuse, check out, bring up, then hand the terminal
// to Claude. It runs the OTHER commands' work functions (newWork, checkoutWork,
// upWork, runClaudeSession); nothing about creation, ensuring, starting or
// session policy is re-implemented here, so `launch` cannot drift from the
// commands it stands in for.
//
// PHASES, in order, and the abort rule (the decided row): any phase failing
// stops the sequence and returns that phase's error — its kind picks the exit
// code — so Claude is NEVER launched onto a broken environment. The create path
// is transactional (newWork undoes itself), while the reuse path converges: a
// failure there leaves the workspace in place for a re-run, exactly as
// `checkout`/`up` would.
//
//  1. resolve the task id in the registry:
//     FOUND    → reuse. Prints `using existing workspace <name>`; any projects
//     listed after the description are checked out (checkoutWork).
//     NOT FOUND → create. The description becomes REQUIRED (usage error, exit
//     2, when absent) and newWork runs with the listed projects.
//     Anything else (an ambiguous task id) is that error, verbatim.
//  2. up on the WHOLE workspace (ResolveTargets with no targets) — not just the
//     projects named on this command line: `launch` promises a workspace that
//     is running, and a project checked out by an earlier invocation is part of
//     it.
//  3. the session, via the same runner as `workspace claude`.
//
// REUSE ignores a supplied description SILENTLY — v1 launch is
// idempotent-reuse, and the `using existing workspace <name>` line is the
// notice: it names the workspace that actually got used, which is the only fact
// a mismatched description could have confused. The grammar does not change
// between the paths either: positional 2 is ALWAYS the description slot, so
// extra projects follow it in both.
//
// DisableFlagParsing, as `claude` (spec §8) — the same three costs handled the
// same way (hand-written arg checks, -h answered here, a leading flag-looking
// token refused by requireIdentFirst). One addition: launch has positionals of
// its OWN after the flags, so extraction happens in two steps —
// splitPassthrough first (everything after `--` is claude's, never a
// positional), then extractSessionFlags over the region before it (so -S/-R
// cannot be mistaken for a description or a project name).
func newLaunchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "launch <task_id> [<description> [project...]] [-S] [-R] [-- claude args...]",
		Short: "Create or reuse a workspace, start it, and open a Claude session",
		Long: "Create or reuse a workspace, check out projects, start daemons, " +
			"then launch a Claude Code session in it.\n\n" +
			"An existing workspace is reused as-is: the description is ignored " +
			"and any projects listed after it are checked out.",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			if err := requireIdentFirst("launch", "task id", args); err != nil {
				return err
			}
			pre, claudeArgs := splitPassthrough(args)
			skipPerms, noResume, positional := extractSessionFlags(pre)
			if len(positional) == 0 {
				return xerr.Wrap(xerr.ErrUsage, errors.New("a task id is required"))
			}
			taskID := positional[0]
			if err := requireValidTaskID(taskID); err != nil {
				return err
			}

			root, cfg, reg, err := loadRootDir()
			if err != nil {
				return err
			}

			// --- phase 1: create or reuse --------------------------------
			out := cmd.OutOrStdout()
			ws, err := wsp.Resolve(reg, taskID)
			switch {
			case err == nil:
				fmt.Fprintf(out, "using existing workspace %s\n", ws.Name())
				// The one description the silent-ignore rule must NOT stay
				// silent about: a supplied description that exactly names a
				// configured project is almost certainly `launch <id>
				// <project>` typed with the description slot forgotten — and
				// the user would otherwise get a session with that project
				// still not checked out and nothing on screen saying so. The
				// grammar does not bend (positional 2 is the description); the
				// note just makes the drop visible.
				if len(positional) > 1 {
					if _, isProject := cfg.Projects[positional[1]]; isProject {
						fmt.Fprintf(out, "note: %q is in the description slot and was ignored"+
							" — projects go after a description (launch <id> <desc> <project…>)"+
							" or use checkout\n", positional[1])
					}
				}
				if len(positional) > 2 {
					if err := checkoutWork(cfg, ws, positional[2:]); err != nil {
						return err
					}
				}
			case errors.Is(err, xerr.ErrNotFound):
				if len(positional) < 2 {
					return xerr.Wrap(xerr.ErrUsage, fmt.Errorf(
						"no workspace for task %q yet, so a description is required: workspace launch %s <description> [project...]",
						taskID, taskID))
				}
				if ws, err = newWork(cmd, cfg, root, taskID, positional[1], positional[2:]); err != nil {
					return err
				}
			default:
				return err // an ambiguous task id: plain error, exit 1
			}

			// --- phase 2: up ---------------------------------------------
			work, err := wsp.ResolveTargets(cfg, ws, nil)
			if err != nil {
				return err
			}
			if len(work) == 0 {
				// A workspace with nothing checked out is a legitimate place
				// to think in, so this is not an abort — but the hint (up's
				// own) says how to give it a stack.
				hintNothingCheckedOut(cmd, ws)
			} else if err := upWork(cmd, cfg, ws, work); err != nil {
				return err
			}

			// --- phase 3: the session ------------------------------------
			return runClaudeSession(cfg, ws, skipPerms, noResume, claudeArgs)
		},
	}
}

// splitPassthrough splits args at the FIRST literal `--`: pre is launch's own
// positional region, post is claude's verbatim passthrough (the separator
// dropped, any later `--` kept — it belongs to claude too). No separator means
// no passthrough.
//
// `claude` needs no such split (everything after its workspace argument is
// already claude's); launch does, because its positionals and claude's
// arguments would otherwise be indistinguishable — extractSessionFlags merges
// the two regions into one rest, which is exactly right for `claude` and
// exactly wrong here.
func splitPassthrough(args []string) (pre, post []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}
