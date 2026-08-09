package cli_test

import (
	"os"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"git.internal/cat/claude-workspaces-go/internal/cli"
)

// TestMain installs "workspace" as a command available inside the txtar
// scripts, running cli.Main in a subprocess of the test binary. testscript.Main
// replaces the deprecated RunMain (go-internal v1.15: Go collects integration
// coverage natively, so returning an exit code became pointless); the command
// func returns no int, so cli.Main's code reaches the script via os.Exit.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"workspace": func() { os.Exit(cli.Main()) },
	})
}

// TestScripts runs every txtar script in testdata; each script gets its own
// $WORK dir and an isolated environment. The custom commands below extract the
// preamble every fixture script needs — checkout.txtar was the fourth copy, so
// per the extraction rule the shared setup became helpers. Older scripts keep
// their inline copies on purpose (they predate the rule and re-editing them
// buys nothing); new scripts should start with `wsenv`.
func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata",
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"wsenv":    cmdWsenv,
			"workroot": cmdWorkroot,
		},
	})
}

// cmdWsenv sets the standard script environment: hermetic git (both config
// scopes at /dev/null, identity from the env, so the host's gitconfig cannot
// change what commands report) and the workspaces root at $WORK/root.
func cmdWsenv(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) != 0 {
		ts.Fatalf("usage: wsenv")
	}
	ts.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	ts.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	ts.Setenv("GIT_AUTHOR_NAME", "t")
	ts.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	ts.Setenv("GIT_COMMITTER_NAME", "t")
	ts.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	ts.Setenv("CLAUDE_WORKSPACES_ROOT_DIR", ts.Getenv("WORK")+"/root")
}

// cmdWorkroot copies src to dst substituting the literal WORKROOT with
// $WORK/root. Registry and config fixtures need absolute paths, txtar file
// bodies are extracted verbatim (no env expansion), so path-bearing fixtures
// ship as templates and are instantiated here.
func cmdWorkroot(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) != 2 {
		ts.Fatalf("usage: workroot <src-template> <dst>")
	}
	data := ts.ReadFile(args[0])
	out := strings.ReplaceAll(data, "WORKROOT", ts.Getenv("WORK")+"/root")
	if err := os.WriteFile(ts.MkAbs(args[1]), []byte(out), 0o644); err != nil {
		ts.Fatalf("workroot: %v", err)
	}
}
