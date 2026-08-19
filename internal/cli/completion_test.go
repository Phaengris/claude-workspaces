package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// completionConfig is the fixture the dynamic completers are pinned against:
// two projects, one bare run-and-wait (never a target), one daemon name unique
// across the config (`worker`) and one shared by both projects (`web`, so the
// bare spelling is ambiguous and must NOT be suggested).
const completionConfig = `values:
  PORT: { start: 5000, per_workspace: 10 }
projects:
  app:
    repo: /tmp/app-src
    start:
      - bundle install
      - web: bin/rails s
      - worker: bin/worker
  lib:
    repo: /tmp/lib-src
    start:
      - web: rake serve
`

// completionFixture builds a root holding completionConfig plus two
// allocations, so the workspace-ident completions cover both spellings (full
// name and task id) of more than one workspace and their ordering is
// observable.
func completionFixture(t *testing.T) string {
	t.Helper()
	root := fixtureRoot(t, map[string]string{"config.yml": completionConfig})
	reg := `{"` + filepath.Join(root, "A-1_alpha") + `": {"index": 0, "task_id": "A-1", ` +
		`"description": "alpha", "created_at": "2026-08-01T09:00:00Z", "adopted": false}, ` +
		`"` + filepath.Join(root, "B-2_beta") + `": {"index": 1, "task_id": "B-2", ` +
		`"description": "beta", "created_at": "2026-08-02T09:00:00Z", "adopted": false}}`
	if err := os.WriteFile(filepath.Join(root, ".allocations.json"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// complete drives cobra's own completion protocol — the hidden `__complete`
// command a shell script invokes — and returns the suggestions plus the
// directive. Driving the protocol rather than calling the completion functions
// directly is what makes these tests contracts about the SHELL's view: the arg
// slot each function sees, the prefix filtering, and the directive all come
// from cobra's plumbing, not from ours.
func complete(t *testing.T, args ...string) ([]string, cobra.ShellCompDirective) {
	t.Helper()
	root := Root()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard) // cobra narrates the directive on stderr
	root.SetArgs(append([]string{cobra.ShellCompRequestCmd}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("__complete %v: unexpected error %v (completions must never fail)", args, err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, ":") {
		t.Fatalf("__complete %v: last line %q is not a directive", args, last)
	}
	code, err := strconv.Atoi(strings.TrimPrefix(last, ":"))
	if err != nil {
		t.Fatalf("__complete %v: bad directive %q: %v", args, last, err)
	}
	return lines[:len(lines)-1], cobra.ShellCompDirective(code)
}

// assertCompletions compares suggestions and directive exactly.
func assertCompletions(t *testing.T, args []string, want []string, wantDirective cobra.ShellCompDirective) {
	t.Helper()
	got, directive := complete(t, args...)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("__complete %v = %v, want %v", args, got, want)
	}
	if directive != wantDirective {
		t.Errorf("__complete %v directive = %d, want %d", args, directive, wantDirective)
	}
}

// wsIdents is the fixture's completed workspace set: full names ONLY, sorted.
// Task ids still resolve (wsp.Resolve accepts both spellings) but are not
// OFFERED: an id is by construction a prefix of its name (`new` builds
// <id>_<slug>, `adopt`'s id IS the dir basename), so offering both listed
// every workspace twice while a typed id-prefix reaches the name anyway.
var wsIdents = []string{"A-1_alpha", "B-2_beta"}

// TestDynamicCompletions pins one case per completed argument slot in the
// command tree.
func TestDynamicCompletions(t *testing.T) {
	const noFile = cobra.ShellCompDirectiveNoFileComp
	cases := map[string]struct {
		args      []string
		want      []string
		directive cobra.ShellCompDirective
	}{
		// The workspace slot: names and task ids, prefix-filtered.
		"cd workspace":        {args: []string{"cd", ""}, want: wsIdents, directive: noFile},
		"cd workspace prefix": {args: []string{"cd", "B"}, want: []string{"B-2_beta"}, directive: noFile},
		// Case-insensitive: the real-use miss was `cd try<TAB>` offering no
		// TRY-* workspaces. The shell replaces the whole token with the
		// candidate, so completing across case is safe everywhere.
		"cd workspace prefix lowercase": {args: []string{"cd", "b"}, want: []string{"B-2_beta"}, directive: noFile},
		"cd project prefix upcase":      {args: []string{"cd", "A-1", "L"}, want: []string{"lib"}, directive: noFile},
		"status workspace":              {args: []string{"status", ""}, want: wsIdents, directive: noFile},
		"destroy workspace":             {args: []string{"destroy", ""}, want: wsIdents, directive: noFile},
		"up workspace":                  {args: []string{"up", ""}, want: wsIdents, directive: noFile},
		"logs workspace":                {args: []string{"logs", ""}, want: wsIdents, directive: noFile},
		"exec workspace":                {args: []string{"exec", ""}, want: wsIdents, directive: noFile},
		// Session commands parse their own flags, so cobra hands the completer
		// the raw argv: only the first slot is reliably identifiable.
		"claude workspace":  {args: []string{"claude", ""}, want: wsIdents, directive: noFile},
		"launch workspace":  {args: []string{"launch", ""}, want: wsIdents, directive: noFile},
		"claude past first": {args: []string{"claude", "A-1", ""}, want: nil, directive: noFile},
		// launch's positional grammar IS re-parsed for completion (the pure
		// helpers exist anyway): task id, then description (free text — no
		// suggestions), then project names minus the ones already typed.
		// -S/-R don't shift the positional count; after `--` everything
		// belongs to claude.
		"launch flag then ws":     {args: []string{"launch", "-S", ""}, want: wsIdents, directive: noFile},
		"launch desc slot":        {args: []string{"launch", "PATFIX", ""}, want: nil, directive: noFile},
		"launch projects":         {args: []string{"launch", "PATFIX", "little fixes", ""}, want: []string{"app", "lib"}, directive: noFile},
		"launch projects prefix":  {args: []string{"launch", "PATFIX", "little fixes", "a"}, want: []string{"app"}, directive: noFile},
		"launch minus typed":      {args: []string{"launch", "PATFIX", "little fixes", "app", ""}, want: []string{"lib"}, directive: noFile},
		"launch flags interleave": {args: []string{"launch", "-S", "PATFIX", "-R", "little fixes", ""}, want: []string{"app", "lib"}, directive: noFile},
		"launch past dashdash":    {args: []string{"launch", "PATFIX", "little fixes", "--", ""}, want: nil, directive: noFile},

		// Project slots: configured project names.
		"cd project":          {args: []string{"cd", "A-1", ""}, want: []string{"app", "lib"}, directive: noFile},
		"cd project prefix":   {args: []string{"cd", "A-1", "l"}, want: []string{"lib"}, directive: noFile},
		"env project":         {args: []string{"env", "A-1", ""}, want: []string{"app", "lib"}, directive: noFile},
		"browse project":      {args: []string{"browse", "A-1", ""}, want: []string{"app", "lib"}, directive: noFile},
		"checkout project":    {args: []string{"checkout", "A-1", ""}, want: []string{"app", "lib"}, directive: noFile},
		"checkout project n":  {args: []string{"checkout", "A-1", "app", ""}, want: []string{"app", "lib"}, directive: noFile},
		"cd past project":     {args: []string{"cd", "A-1", "app", ""}, want: nil, directive: noFile},
		"status past ws":      {args: []string{"status", "A-1", ""}, want: nil, directive: noFile},
		"adopt projects flag": {args: []string{"adopt", "--projects", ""}, want: []string{"app", "lib"}, directive: noFile},
		// A slice flag's value is a comma list: complete the last element and
		// carry the ones already listed (which are not suggested again).
		"adopt projects list": {args: []string{"adopt", "--projects", "app,"}, want: []string{"app,lib"}, directive: noFile},

		// Daemon-target slots (up/down/restart): projects, project:daemon keys,
		// and bare daemon names that are unambiguous. `web` is deliberately
		// absent — two projects declare it.
		"down targets":        {args: []string{"down", "A-1", ""}, want: []string{"app", "app:web", "app:worker", "lib", "lib:web", "worker"}, directive: noFile},
		"up targets prefix":   {args: []string{"up", "A-1", "app:"}, want: []string{"app:web", "app:worker"}, directive: noFile},
		"restart targets":     {args: []string{"restart", "A-1", "w"}, want: []string{"worker"}, directive: noFile},
		"down second target":  {args: []string{"down", "A-1", "app", "lib"}, want: []string{"lib", "lib:web"}, directive: noFile},
		"targets unknown ws":  {args: []string{"down", "NOPE-9", ""}, want: nil, directive: noFile},
		"targets ambiguous w": {args: []string{"down", "A-1", "we"}, want: nil, directive: noFile},

		// logs takes exactly one DAEMON: a project name is a usage error there
		// (soleDaemon), so only daemon spellings are suggested.
		"logs daemon":      {args: []string{"logs", "A-1", ""}, want: []string{"app:web", "app:worker", "lib:web", "worker"}, directive: noFile},
		"logs past daemon": {args: []string{"logs", "A-1", "worker", ""}, want: nil, directive: noFile},

		// No-argument commands complete nothing at all — least of all files.
		"ls":     {args: []string{"ls", ""}, want: nil, directive: noFile},
		"ports":  {args: []string{"ports", ""}, want: nil, directive: noFile},
		"which":  {args: []string{"which", ""}, want: nil, directive: noFile},
		"doctor": {args: []string{"doctor", ""}, want: nil, directive: noFile},
		"gc":     {args: []string{"gc", ""}, want: nil, directive: noFile},
		// `new` names a workspace that does not exist yet: nothing to suggest.
		"new": {args: []string{"new", ""}, want: nil, directive: noFile},

		// The two directory positionals get directory completion.
		"adopt dir":   {args: []string{"adopt", ""}, want: nil, directive: cobra.ShellCompDirectiveFilterDirs},
		"release dir": {args: []string{"release", ""}, want: nil, directive: cobra.ShellCompDirectiveFilterDirs},

		// exec's second slot is either a project or the command, so it offers
		// the project names AND lets the shell complete files (default
		// directive); everything after it belongs to the command.
		"exec project or command": {args: []string{"exec", "A-1", ""}, want: []string{"app", "lib"}, directive: cobra.ShellCompDirectiveDefault},
		"exec command args":       {args: []string{"exec", "A-1", "app", "ls", ""}, want: nil, directive: cobra.ShellCompDirectiveDefault},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			completionFixture(t)
			assertCompletions(t, tc.args, tc.want, tc.directive)
		})
	}
}

// TestCompletionsSurviveBrokenRoot pins the safety contract: a root that no
// command could run in (no config, invalid config, unreadable registry) yields
// NO suggestions, NO error and NO file fallback. A completion function that
// propagated its error would make every <TAB> in the shell print a diagnostic
// over the user's prompt.
func TestCompletionsSurviveBrokenRoot(t *testing.T) {
	roots := map[string]map[string]string{
		"missing config":  nil,
		"invalid config":  {"config.yml": "values:\n  PORT: { start: -1, per_workspace: 0 }\n"},
		"broken registry": {"config.yml": validConfig, ".allocations.json": "{ not json"},
	}
	argsets := [][]string{
		{"cd", ""},
		{"cd", "A-1", ""},
		{"checkout", "A-1", ""},
		{"down", "A-1", ""},
		{"logs", "A-1", ""},
		{"claude", ""},
		{"adopt", "--projects", ""},
	}
	for name, files := range roots {
		for _, args := range argsets {
			t.Run(name+"/"+strings.Join(args, "_"), func(t *testing.T) {
				fixtureRoot(t, files)
				assertCompletions(t, args, nil, cobra.ShellCompDirectiveNoFileComp)
			})
		}
	}
}

// TestCompletionScripts pins that the generated scripts exist and are the real
// thing (cobra's generators, kept rather than hand-rolled). The sentinels are
// the entry points each shell needs, so a truncated or wrong-shell script
// fails here.
func TestCompletionScripts(t *testing.T) {
	cases := map[string]string{
		"bash": "complete -o default -F __start_workspace workspace",
		"zsh":  "#compdef workspace",
		"fish": "function __workspace_perform_completion",
	}
	for shell, sentinel := range cases {
		t.Run(shell, func(t *testing.T) {
			root := Root()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(io.Discard)
			root.SetArgs([]string{"completion", shell})
			if err := root.Execute(); err != nil {
				t.Fatalf("completion %s: %v", shell, err)
			}
			if !strings.Contains(out.String(), sentinel) {
				t.Errorf("completion %s script (%d bytes) does not contain %q", shell, out.Len(), sentinel)
			}
			// The script must also carry the dynamic-completion request, which
			// is what makes the wiring above reach the shell.
			if !strings.Contains(out.String(), cobra.ShellCompRequestCmd) {
				t.Errorf("completion %s script does not request dynamic completions (%q)", shell, cobra.ShellCompRequestCmd)
			}
		})
	}
}

// TestEveryCommandCompletes is the drift guard: every command in the tree has a
// deliberate completion decision, so a command added later cannot silently fall
// back to completing FILE NAMES in a workspace slot. cobra's own `completion`
// and `help` are exempt — they complete themselves.
func TestEveryCommandCompletes(t *testing.T) {
	for _, sub := range Root().Commands() {
		switch sub.Name() {
		case "completion", "help":
			continue
		}
		if sub.ValidArgsFunction == nil {
			t.Errorf("command %q has no ValidArgsFunction: wire it in completion.go (completeNothing if it takes no arguments)", sub.Name())
		}
	}
}
