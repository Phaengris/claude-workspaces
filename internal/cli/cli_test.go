package cli_test

import (
	"os"
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
// $WORK dir and an isolated environment.
func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{Dir: "testdata"})
}
