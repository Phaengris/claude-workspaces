package cli

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// newClaudeCmd builds `workspace claude <workspace> [-S] [-R] [claude args…]`:
// launch a Claude Code session in the workspace dir with the decided flag
// injection (spec §8) — `--dangerously-skip-permissions` unless the user opted
// out or took their own permission stance, `--continue` when a previous
// conversation exists for this dir and nothing suppresses resuming.
//
// DisableFlagParsing, by design (spec §8): every flag except our two (-S/-R,
// scanned only before a literal `--`) belongs to claude, and teaching cobra
// which is which is a losing fight. The costs are handled by hand:
//   - no Args validator runs (cobra's execute() skips nothing, but with
//     parsing off argWoFlags is the raw args and OUR validator would see flag
//     strings) — the missing-workspace usage error (exit 2) is the explicit
//     len check below, not cobra's;
//   - cobra never sees a help flag, so bare -h/--help in the workspace slot is
//     answered here before resolving (it would otherwise be exit 3);
//   - the root's persistent --json is inert AFTER the workspace name (it is
//     claude's argument there, passed through). BEFORE it, any flag-looking
//     token is a usage error: `workspace claude --json T-1` would otherwise
//     resolve the workspace "--json" and fail exit 3 with a message about a
//     missing workspace — see requireIdentFirst.
//
// ENVIRONMENT — the loud distinction (M4 plan, Global Constraints): commands
// the tool runs FOR the user (setup, exec, daemons) get the CURATED allowlist
// env (wsp.CommandEnv). The session itself gets the full INHERITED env —
// already SanitizeSelf'd at startup (root's PersistentPreRun, spec §6), so
// version-manager pins are gone — overlaid with the workspace's resolved
// global env and runtime vars. Claude is the operator's tool and needs the
// real login environment; see sessionEnv.
//
// Exit code: claude's own code becomes ours, verbatim, via xerr.Exit — so a
// claude exit 3 is indistinguishable from ErrNotFound (accepted, documented in
// xerr). A missing claude binary is a plain exit-1 error naming PATH.
func newClaudeCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "claude <workspace> [-S] [-R] [claude args...]",
		Short:              "Launch a Claude Code session in the workspace",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			if err := requireIdentFirst("claude", "workspace", args); err != nil {
				return err
			}
			if len(args) == 0 {
				return xerr.Wrap(xerr.ErrUsage, errors.New("a workspace is required"))
			}
			cfg, reg, err := loadRoot()
			if err != nil {
				return err
			}
			ws, err := wsp.Resolve(reg, args[0]) // ErrNotFound → exit 3
			if err != nil {
				return err
			}
			skipPerms, noResume, rest := extractSessionFlags(args[1:])
			return runClaudeSession(cfg, ws, skipPerms, noResume, rest)
		},
	}
}

// requireIdentFirst enforces the shared first-positional rule of the two
// session commands: their identifier (workspace / task id) must come FIRST.
// With DisableFlagParsing a leading flag-looking token would silently become
// that identifier — `workspace claude --json T-1` resolving a workspace named
// "--json" and failing exit 3 "no workspace matching" — so it is classified
// here as what it is: a usage error (exit 2) that names the rule. The bare
// help forms are answered by the callers BEFORE this check, since -h is the
// one flag users legitimately type in that slot.
func requireIdentFirst(cmdName, noun string, args []string) error {
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return xerr.Wrap(xerr.ErrUsage,
			fmt.Errorf("workspace %s requires the %s as its first argument", cmdName, noun))
	}
	return nil
}

// runClaudeSession is the session spawn, shared by `claude` and `launch` (the
// policy inputs come from extractSessionFlags at each entry point; rest is
// already the user's claude args, post-`--` passthrough included). It probes
// history for ws.Dir, builds the argv, runs claude in ws.Dir with INHERITED
// stdio and sessionEnv, and turns the child's exit code into ours.
//
// SIGNALS — why the parent goes deaf while the child runs: claude owns the
// terminal, and the shell delivers ^C (SIGINT) and ^\ (SIGQUIT) to the whole
// foreground process GROUP, so this parent receives them too. A parent that
// takes the default action dies on the first ^C, handing the shell back its
// prompt while claude keeps drawing on the same tty — the classic interleaved
// mess. So the parent absorbs both signals for the duration and lets the child,
// which is the one that should decide what ^C means, handle them.
//
// signal.Notify, NOT signal.Ignore: Ignore sets the disposition to SIG_IGN, and
// SIG_IGN SURVIVES exec — the child would inherit it and become literally
// uninterruptible (^C would never reach claude). Notify changes only THIS
// process's handling; Go resets Notify'd signals to their default in a forked
// child, so claude's own handling is intact. The channel is deliberately never
// read: Notify's send is non-blocking, so notifications past the buffered one
// are dropped on the floor — absorbing is the whole job. signal.Stop restores
// default behavior once the session ends, so a ^C at OUR prompt still kills us.
func runClaudeSession(cfg *config.Config, ws wsp.Workspace, skipPerms, noResume bool, rest []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "" // probe degrades to "no history"; a fresh session still launches
	}
	argv := buildClaudeArgv(rest, skipPerms, noResume, hasConversation(home, ws.Dir))

	// LookPath honors THIS process's PATH — right here, unlike exec: the
	// session runs under the inherited env, and that PATH was already
	// sanitized at startup.
	bin, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude binary not found in PATH — install Claude Code or fix PATH")
	}
	c := exec.Command(bin, argv...)
	c.Dir = ws.Dir
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	c.Env = sessionEnv(cfg, ws)

	absorbed := make(chan os.Signal, 1)
	signal.Notify(absorbed, syscall.SIGINT, syscall.SIGQUIT)
	defer signal.Stop(absorbed)

	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		// ExitCode() is -1 when the child died from a signal rather than
		// exiting; that is not a code to propagate — fall through to the plain
		// exit-1 error, which at least says what happened.
		if errors.As(err, &ee) && ee.ExitCode() >= 0 {
			return xerr.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}

// extractSessionFlags splits the tool's session flags out of the raw args.
// Only the region BEFORE the first literal `--` is scanned: there, -S/
// --claude-no-skip-permissions clears skipPerms and -R/--claude-no-resume sets
// noResume, and both are removed from rest. The first `--` is dropped and
// everything after it — including strings spelled exactly like our flags, and
// any later `--` — is appended to rest verbatim: it is claude's, untouched.
//
// The two results are POLICY, not flag presence, so buildClaudeArgv reads
// naturally: skipPerms defaults to TRUE (inject --dangerously-skip-permissions)
// and -S turns it off; noResume defaults to FALSE (resuming allowed) and -R
// turns it on.
func extractSessionFlags(args []string) (skipPerms, noResume bool, rest []string) {
	skipPerms = true
	for i, a := range args {
		switch a {
		case "--":
			rest = append(rest, args[i+1:]...)
			return skipPerms, noResume, rest
		case "-S", "--claude-no-skip-permissions":
			skipPerms = false
		case "-R", "--claude-no-resume":
			noResume = true
		default:
			rest = append(rest, a)
		}
	}
	return skipPerms, noResume, rest
}

// buildClaudeArgv assembles claude's argument vector (without argv[0]; the
// caller hands it to exec.Command): injected flags first, then rest verbatim.
// The injection matrix (spec §8, the decided row):
//
//	--dangerously-skip-permissions  iff skipPerms (no -S)
//	                                AND rest carries no user permission stance
//	                                (--permission-mode[=…] or
//	                                --dangerously-skip-permissions itself);
//	--continue                      iff hasHistory (a conversation exists for
//	                                the workspace dir) AND NOT noResume (-R)
//	                                AND rest carries neither print mode
//	                                (-p/--print — a resumed interactive session
//	                                makes no sense for one-shot output) nor a
//	                                user resume flag (-c/--continue/-r/
//	                                --resume[=…]/--from-pr[=…]).
//
// The scan covers ALL of rest — including what came after `--`, which extract
// merged in verbatim: a flag is the user's stance no matter which side of the
// separator it was typed on, and injecting alongside it would double or fight
// it. Value-taking long flags are also detected in their --flag=value
// spelling; the plan's list names --permission-mode= explicitly, and --resume=/
// --from-pr= are the same shape (a superset, documented here: a user spelling
// --resume=abc is unambiguously resuming, and an injected --continue would
// fight it).
func buildClaudeArgv(rest []string, skipPerms, noResume, hasHistory bool) []string {
	userPerm, userResume, printMode := false, false, false
	for _, a := range rest {
		switch {
		case a == "--dangerously-skip-permissions",
			a == "--permission-mode", strings.HasPrefix(a, "--permission-mode="):
			userPerm = true
		case a == "-c", a == "--continue",
			a == "-r", a == "--resume", strings.HasPrefix(a, "--resume="),
			a == "--from-pr", strings.HasPrefix(a, "--from-pr="):
			userResume = true
		case a == "-p", a == "--print":
			printMode = true
		}
	}
	var argv []string
	if skipPerms && !userPerm {
		argv = append(argv, "--dangerously-skip-permissions")
	}
	if hasHistory && !noResume && !printMode && !userResume {
		argv = append(argv, "--continue")
	}
	return append(argv, rest...)
}

// hasConversation reports whether Claude Code has a conversation on record for
// wsDir: ~/.claude/projects/<encoded>/ contains at least one *.jsonl entry.
// Any failure to look (no HOME, no dir, unreadable) is "no history" — the
// probe only ever decides whether --continue is worth injecting, and a fresh
// session is the safe degradation.
//
// TWO paths are probed, in this order: the dir AS WRITTEN (what the registry
// records and what every other command uses), then its EvalSymlinks-resolved
// form when that differs. Claude Code encodes the path it actually ran in, and
// which of the two that is depends on how the session was started — a
// workspaces root reached through a symlink (a linked or relocated
// claude-workspaces dir) yields history under the resolved spelling while
// ws.Dir keeps the as-written one, and a session started from the real path
// yields the opposite. Probing one spelling only would silently drop
// --continue for the other, so both are asked and either answer counts.
// As-written comes FIRST: it is the identity the tool itself uses, so it is the
// likelier hit and the one whose success costs no extra syscalls. A resolution
// failure (dangling link, permissions) simply leaves the second probe unasked —
// the same safe degradation as everything else here.
func hasConversation(home, wsDir string) bool {
	if conversationRecorded(home, wsDir) {
		return true
	}
	resolved, err := filepath.EvalSymlinks(wsDir)
	if err != nil || resolved == wsDir {
		return false // nothing new to probe (deduped when identical)
	}
	return conversationRecorded(home, resolved)
}

// conversationRecorded is one probe: does ~/.claude/projects/<encoded dir>/
// hold at least one *.jsonl entry?
func conversationRecorded(home, dir string) bool {
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(dir)))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			return true
		}
	}
	return false
}

// encodeClaudeProjectDir maps an absolute dir to Claude Code's per-project
// memory dir name: every byte outside [A-Za-z0-9] becomes '-'.
//
// VERIFIED EMPIRICALLY (2026-08-10, this machine's ~/.claude/projects): the
// M4 plan stated the narrower "/ and . become -", but the real listing shows
//
//	/home/cat/claude-workspaces/PATADM_patternima-admin-panel
//	  → -home-cat-claude-workspaces-PATADM-patternima-admin-panel
//
// — the UNDERSCORE was replaced too, so the rule is all-non-alphanumeric.
// This matters daily: this tool's own workspace dirs are named <task>_<slug>,
// so the narrower rule would never find their history and --continue would
// never inject. No dotted-path example existed on this machine, but dots are
// non-alphanumeric and the general rule covers them. claude.txtar re-derives
// this encoding independently (sed) so a drift in either direction fails.
func encodeClaudeProjectDir(dir string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, dir)
}

// sessionEnv composes the Claude session's environment — the OTHER spawn
// contract (M4 plan, Global Constraints), distinct from wsp.CommandEnv's
// curated allowlist. Base: os.Environ(), i.e. the full inherited login env,
// already SanitizeSelf'd at startup so version-manager activation is gone.
// Overlay, winning per key: the workspace's resolved global env
// (wsp.ResolvedEnv — config `env` with runtime tokens substituted, no project)
// plus the runtime vars themselves (WORKSPACE and the numbered value vars,
// PORT0…), exported as real env vars — v1 exported both, and session-side
// scripts rely on $WORKSPACE/$PORT0 existing even when config `env` never
// mentions them. RuntimeVars is applied last: the tool-derived identity vars
// are authoritative over a config env entry that happens to reuse a name.
// Output is sorted for determinism, like envx.Curated.
func sessionEnv(cfg *config.Config, ws wsp.Workspace) []string {
	merged := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			merged[k] = v
		}
	}
	maps.Copy(merged, wsp.ResolvedEnv(cfg, ws.Alloc.TaskID, "", ws.Alloc.Index))
	maps.Copy(merged, wsp.RuntimeVars(cfg, ws.Alloc.TaskID, "", ws.Alloc.Index))
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}
