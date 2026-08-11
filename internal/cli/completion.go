package cli

import (
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

// Shell completions (spec §2). Two halves:
//
//   - The SCRIPTS come from cobra: `workspace completion bash|zsh|fish` is
//     cobra's own generated subcommand, deliberately kept rather than
//     hand-rolled — the scripts are the client side of cobra's `__complete`
//     protocol and hand-maintaining them would mean re-deriving that protocol
//     on every cobra upgrade. Nothing in this file registers the command; it
//     exists because the tree has subcommands and completion is not disabled.
//     M5's installer writes the generated fish script to fish's
//     `completions/` directory; `functions/workspace.fish` is the cd WRAPPER
//     and must not be overwritten with a completion script.
//
//   - The dynamic SUGGESTIONS live here: one ValidArgsFunction per command,
//     wired in one table (wireCompletions) so the whole completion policy of
//     the tool reads top-to-bottom in one place instead of being spread over
//     twenty command constructors. TestEveryCommandCompletes is the drift
//     guard: a command with no entry has no ValidArgsFunction and fails the
//     test, which is what stops a new command from silently completing FILE
//     NAMES where a workspace name belongs.
//
// THE SAFETY RULE, obeyed by every function below: a completer never reports a
// failure. Config unreadable, config invalid, registry corrupt, workspace
// unresolvable — all of it collapses to "no suggestions" with
// ShellCompDirectiveNoFileComp. A completer runs on every <TAB> the user
// presses, in a shell whose prompt it does not own; an error message printed
// there is worse than silence, and a file-name fallback in a workspace slot is
// worse still (it suggests strings no command accepts).

// wireCompletions attaches a completion function to every command in the tree.
// Called by Root after the commands are registered; keyed by Name(), so
// aliases (`down`/`stop`) are covered by their command's single entry.
func wireCompletions(root *cobra.Command) {
	byName := map[string]cobra.CompletionFunc{
		// Query commands taking no positionals: nothing to suggest, and
		// explicitly not files.
		"ls":     completeNothing,
		"ports":  completeNothing,
		"which":  completeNothing,
		"doctor": completeNothing,
		"gc":     completeNothing,
		// `new` names a workspace that does not exist yet — its task id and
		// description are free text, so there is nothing to suggest for either.
		"new": completeNothing,
		// install/uninstall take no positionals; their targets are fixed
		// paths under $HOME, not anything worth suggesting.
		"install":   completeNothing,
		"uninstall": completeNothing,

		// A single workspace slot.
		"status":  completeWorkspace,
		"destroy": completeWorkspace,

		// Workspace, then one optional project.
		"cd":     completeWorkspaceThenProject,
		"env":    completeWorkspaceThenProject,
		"browse": completeWorkspaceThenProject,

		// Workspace, then any number of projects.
		"checkout": completeWorkspaceThenProjects,

		// Workspace, then daemon targets (projects and daemons alike).
		"up":      completeWorkspaceThenTargets,
		"down":    completeWorkspaceThenTargets,
		"restart": completeWorkspaceThenTargets,

		// Workspace, then exactly one daemon.
		"logs": completeWorkspaceThenDaemon,

		// Workspace, then a project-or-command slot, then the command's own
		// arguments.
		"exec": completeExec,

		// A directory positional.
		"adopt":   completeDir,
		"release": completeDir,

		// Sessions: claude completes its workspace slot only (everything
		// after it belongs to claude itself); launch re-parses its own
		// positional grammar (see completeLaunch).
		"claude": completeSessionWorkspace,
		"launch": completeLaunch,
	}
	for _, cmd := range root.Commands() {
		if fn, ok := byName[cmd.Name()]; ok {
			cmd.ValidArgsFunction = fn
		}
		// `adopt --projects` is the one FLAG whose value is a set of project
		// names. The error can only mean the flag was renamed out from under
		// this table; the completion test pins the wiring, so swallowing it
		// here keeps Root() free of an error path that cannot fire in a
		// working build.
		if cmd.Name() == "adopt" {
			_ = cmd.RegisterFlagCompletionFunc("projects", completeProjectsFlag)
		}
	}
}

// completeNothing suggests nothing and suppresses file completion. It is the
// deliberate answer for the commands that take no positionals — the shell
// offering file names after `workspace ls` would be pure noise.
func completeNothing(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// completeDir asks the shell to complete DIRECTORIES for the first positional
// and nothing after it. `adopt [dir]` and `release [dir]` take a filesystem
// path, not an identifier — this is the one place where letting the shell do
// its own thing is the right answer. (Both also accept no argument at all, in
// which case they act on the cwd; that needs no completion.)
func completeDir(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveFilterDirs
}

// completeWorkspace completes the single workspace slot of `status` and
// `destroy`.
func completeWorkspace(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return workspaceIdents(toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeSessionWorkspace completes the workspace slot of `claude`. The
// command sets DisableFlagParsing (spec §8), so cobra hands the completer the
// raw argv; the first slot is the only one that is ours — everything after it
// is claude's own argv, and suggesting anything there would be wrong.
func completeSessionWorkspace(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return workspaceIdents(toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeLaunch re-parses launch's positional grammar for the shell's
// benefit, with the same pure helpers the command itself parses with: strip
// the session flags (-S/-R — they never shift the positional count), stop at
// `--` (everything after is claude passthrough), then complete by position —
// task id (existing workspace idents: an existing id is what makes launch
// reuse), description (free text, no suggestions), then configured project
// names minus the ones already typed.
func completeLaunch(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if slices.Contains(args, "--") {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	_, _, positionals := extractSessionFlags(args)
	switch len(positionals) {
	case 0:
		return workspaceIdents(toComplete), cobra.ShellCompDirectiveNoFileComp
	case 1:
		return nil, cobra.ShellCompDirectiveNoFileComp
	default:
		taken := make(map[string]bool, len(positionals)-2)
		for _, p := range positionals[2:] {
			taken[p] = true
		}
		var names []string
		for _, name := range projectNames(toComplete) {
			if !taken[name] {
				names = append(names, name)
			}
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeWorkspaceThenProject completes `<workspace> [project]`.
func completeWorkspaceThenProject(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return workspaceIdents(toComplete), cobra.ShellCompDirectiveNoFileComp
	case 1:
		return projectNames(toComplete), cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeWorkspaceThenProjects completes `<workspace> <project>...` — every
// slot after the workspace is a project, so `checkout` keeps suggesting them.
func completeWorkspaceThenProjects(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return workspaceIdents(toComplete), cobra.ShellCompDirectiveNoFileComp
	}
	return projectNames(toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeWorkspaceThenTargets completes `<workspace> [target...]` for
// up/down/restart: every slot after the workspace is a target in wsp's grammar
// (a project, a bare daemon name, or `project:daemon`).
func completeWorkspaceThenTargets(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return workspaceIdents(toComplete), cobra.ShellCompDirectiveNoFileComp
	}
	cfg, ok := resolvedFor(args[0])
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := append(slices.Sorted(maps.Keys(cfg.Projects)), daemonSpellings(cfg)...)
	slices.Sort(names)
	return matching(slices.Compact(names), toComplete), cobra.ShellCompDirectiveNoFileComp
}

// completeWorkspaceThenDaemon completes `<workspace> <daemon>` for `logs`.
//
// Project names are deliberately NOT suggested even though wsp's target
// grammar accepts them: `logs` narrows a resolved target to exactly one daemon
// (soleDaemon), so a project name there is a usage error. Suggesting a string
// the command then refuses is worse than suggesting nothing.
func completeWorkspaceThenDaemon(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return workspaceIdents(toComplete), cobra.ShellCompDirectiveNoFileComp
	case 1:
		cfg, ok := resolvedFor(args[0])
		if !ok {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return matching(daemonSpellings(cfg), toComplete), cobra.ShellCompDirectiveNoFileComp
	default:
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeExec completes `<workspace> [project] <command> [args...]`.
//
// The second slot is genuinely ambiguous — exec sniffs it as a project when it
// names one and treats it as the command otherwise — so it offers the project
// names AND leaves the shell's own file completion on (ShellCompDirectiveDefault
// adds the shell's behavior to what we return). Everything after it belongs to
// the command being run, whose arguments only the user knows: plain file
// completion, which is what a shell would have done for a bare command line.
func completeExec(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		return workspaceIdents(toComplete), cobra.ShellCompDirectiveNoFileComp
	case 1:
		return projectNames(toComplete), cobra.ShellCompDirectiveDefault
	default:
		return nil, cobra.ShellCompDirectiveDefault
	}
}

// completeProjectsFlag completes `adopt --projects`, whose value is a COMMA
// LIST. The shell replaces the whole word, so each suggestion carries the
// elements already typed; names already in the list are dropped (a repeat would
// be a no-op that hides the names still worth adding).
func completeProjectsFlag(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, _, ok := completionRoot()
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	prefix, last := "", toComplete
	if i := strings.LastIndex(toComplete, ","); i >= 0 {
		prefix, last = toComplete[:i+1], toComplete[i+1:]
	}
	listed := strings.Split(strings.TrimSuffix(prefix, ","), ",")
	var out []string
	for _, name := range matching(slices.Sorted(maps.Keys(cfg.Projects)), last) {
		if slices.Contains(listed, name) {
			continue
		}
		out = append(out, prefix+name)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completionRoot is the completers' shared preamble: the same load every
// command performs, with every failure turned into "no data" (see THE SAFETY
// RULE above). It is the only place a completer touches the filesystem.
func completionRoot() (*config.Config, alloc.Registry, bool) {
	cfg, reg, err := loadRoot()
	if err != nil {
		return nil, nil, false
	}
	return cfg, reg, true
}

// resolvedFor loads the config for a slot whose suggestions only make sense
// inside a KNOWN workspace: the ident (the command's first argument) must
// resolve, or there is nothing to complete. An unresolvable ident means the
// user is still typing (or mistyped) the workspace, and suggesting the config's
// targets under it would offer strings the command is certain to reject.
func resolvedFor(ident string) (*config.Config, bool) {
	cfg, reg, ok := completionRoot()
	if !ok {
		return nil, false
	}
	if _, err := wsp.Resolve(reg, ident); err != nil {
		return nil, false
	}
	return cfg, true
}

// workspaceIdents lists every string wsp.Resolve accepts — each workspace's
// full name and its task id — sorted and filtered by what is typed so far.
// Both spellings are offered because both work, and the short one (the task id)
// is what a user types by hand.
//
// No descriptions are attached: a completion's description is display-only
// noise for identifiers this short, and workspace descriptions are free text
// that would need per-shell escaping.
func workspaceIdents(toComplete string) []string {
	_, reg, ok := completionRoot()
	if !ok {
		return nil
	}
	idents := map[string]bool{}
	for _, ws := range wsp.List(reg) {
		idents[ws.Name()] = true
		if ws.Alloc.TaskID != "" {
			idents[ws.Alloc.TaskID] = true
		}
	}
	return matching(slices.Sorted(maps.Keys(idents)), toComplete)
}

// projectNames lists the CONFIGURED project names, sorted and prefix-filtered.
// Configured, not checked-out: `checkout` and `up` create what is missing, and
// `cd`/`env` answer for a project whether or not this workspace has it yet
// (their own doc comments make that explicit). Template-expanded projects are
// ordinary config entries by the time this runs, so they complete like any
// other.
func projectNames(toComplete string) []string {
	cfg, _, ok := completionRoot()
	if !ok {
		return nil
	}
	return matching(slices.Sorted(maps.Keys(cfg.Projects)), toComplete)
}

// daemonSpellings lists every string that names exactly one daemon in wsp's
// target grammar, sorted: every `project:daemon` key, plus the bare daemon
// names that are unambiguous.
//
// A bare name is offered only when ONE project declares it and no project is
// called that — the two cases wsp.ResolveTargets would not resolve to that
// daemon (a repeated name is an error listing the keys; a project of the same
// name wins the match). The `project:daemon` spelling of such a daemon is
// always offered, so nothing becomes unreachable by completion.
func daemonSpellings(cfg *config.Config) []string {
	count := map[string]int{}
	var out []string
	for _, project := range slices.Sorted(maps.Keys(cfg.Projects)) {
		for _, d := range wsp.DaemonsOf(cfg, project) {
			out = append(out, d.Key())
			count[d.Name]++
		}
	}
	for name, n := range count {
		if n == 1 && cfg.Projects[name] == nil {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// matching keeps the candidates the user's partial word could still become.
// Cobra hands the completions to the shell as-is, so this filtering is ours to
// do (some shells filter again, none can be relied on to).
func matching(names []string, toComplete string) []string {
	var out []string
	for _, name := range names {
		if strings.HasPrefix(name, toComplete) {
			out = append(out, name)
		}
	}
	return out
}
