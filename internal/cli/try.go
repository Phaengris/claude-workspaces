package cli

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Phaengris/claude-workspaces/internal/alloc"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
	"github.com/Phaengris/claude-workspaces/internal/xerr"
)

// newTryCmd builds `workspace try <description words…> [-S] [-R] [-- claude
// args…]`: one command to a thinking room. It is launch's create path minus
// the ceremony — no projects, and the task id is generated (`TRY-<n>`) — for
// the draft/discussion that precedes real work (the "legitimate place to
// think in" the empty-workspace hint has always acknowledged). The workspace
// is ordinary in every way: allocated values (a draft's scratch server has
// its ports the moment it wants them), WORKSPACE.md, the full lifecycle, and
// `checkout` graduates it into project work in place.
//
// The description is REQUIRED, and it is every positional word joined — no
// quoting, because `try websocket reconnect draft` at the moment of least
// patience must not demand shell syntax. Required, because the name is the
// recall handle: a generated cute name (the docker approach was considered)
// recalls nothing, which is the same reason launch refuses to create without
// a description.
//
// DisableFlagParsing, as claude/launch (spec §8): -S/-R are sniffed by
// extractSessionFlags from the region before `--`, everything after `--` is
// claude's verbatim. A leading flag-looking token is NOT refused here unlike
// launch — there is no identifier slot for it to be confused with; an
// unknown flag before `--` simply joins the description, which is the cost
// of unquoted free text and is visible immediately in the created name.
func newTryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "try <description...> [-S] [-R] [-- claude args...]",
		Short: "Create a projectless draft workspace (TRY-<n>) and open a session in it",
		Long: "Create a workspace with no projects checked out — a place to think, " +
			"discuss or draft before real work — and open a Claude Code session in it.\n\n" +
			"The task id is generated (TRY-<n>); the description is required and is " +
			"all the words you type (no quotes needed). Graduate the draft later with " +
			"`workspace checkout`, or `workspace destroy` it.",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			pre, claudeArgs := splitPassthrough(args)
			skipPerms, noResume, words := extractSessionFlags(pre)
			if len(words) == 0 {
				return xerr.Wrap(xerr.ErrUsage, errors.New(
					"a description is required — it is how you will recall the draft: workspace try <description words...>"))
			}

			root, cfg, reg, err := loadRootDir()
			if err != nil {
				return err
			}
			ws, err := newWork(cmd, cfg, root, nextTryID(root, reg), strings.Join(words, " "), nil)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "tip: in another terminal: workspace cd %s — work alongside this session\n", ws.Alloc.TaskID)
			return runClaudeSession(cfg, ws, skipPerms, noResume, claudeArgs)
		},
	}
}

// tryIDPattern matches the ids `try` generates. Anchored and digits-only:
// a hand-made TRY-foo or TRYOUT-3 is somebody's real task id, not ours to
// count.
var tryIDPattern = regexp.MustCompile(`^TRY-([0-9]+)$`)

// nextTryID returns TRY-<max+1> over every TRY-<n> the root knows of: live
// allocations AND unregistered dirs (a released draft keeps its directory,
// and reusing its number would hand two drafts one name-space — the same
// collision `new` would then refuse, later and more confusingly). Monotonic
// rather than gap-filling on purpose: draft numbers are identities, not a
// scarce resource like allocation indices.
func nextTryID(root string, reg alloc.Registry) string {
	max := 0
	consider := func(id string) {
		if m := tryIDPattern.FindStringSubmatch(id); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > max {
				max = n
			}
		}
	}
	for _, ws := range wsp.List(reg) {
		consider(ws.Alloc.TaskID)
	}
	for _, u := range unregisteredDirs(root, reg) {
		id, _, _ := strings.Cut(u.Name, "_")
		consider(id)
	}
	return "TRY-" + strconv.Itoa(max+1)
}
