package cli

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// browseRoot builds a root whose config's ${PORT0} computes to a port THIS
// TEST is listening on: browse dials before opening (the socket is the
// truth), so a hermetic success path needs a live socket, and borrowing the
// machine's real port 5000 made these tests pass or fail with whatever the
// developer happened to be running (the flake that shipped v1.3.5's red CI).
// The listener is OS-assigned (127.0.0.1:0) and becomes values.PORT's start
// with per_workspace 1, so PORT0 IS the listening port and substitution
// stays exercised. `dead` points one port past it — closed, since the values
// block is the only claim on the range. PATH is emptied so neither git nor
// xdg-open can be found: nothing is checked out (detection degrades to
// "no"), and browse must take the print-the-URL path rather than opening a
// browser on the machine running the tests.
//
// app has a substitutable port, noport has none, dead has a port nobody
// serves — the three thirds of the browse_port contract.
func browseRoot(t *testing.T) (port string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	n := l.Addr().(*net.TCPAddr).Port
	cfg := fmt.Sprintf(`values:
  PORT: { start: %d, per_workspace: 1 }
projects:
  app:
    repo: /tmp/app-src
    browse_port: ${PORT0}
  noport:
    repo: /tmp/noport-src
  dead:
    repo: /tmp/dead-src
    browse_port: "%d"
`, n, n+1)
	root := fixtureRoot(t, map[string]string{"config.yml": cfg})
	reg := `{"` + filepath.Join(root, "A-1_x") + `": {"index": 0, "task_id": "A-1", ` +
		`"description": "d", "created_at": "2026-08-01T09:00:00Z", "adopted": false}}`
	if err := os.WriteFile(filepath.Join(root, ".allocations.json"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	return strconv.Itoa(n)
}

// TestBrowseExitCodes pins browse's split (spec §9): unknown identifiers are 3,
// a defaulting failure (zero checked out — the user must name a project) is a
// usage error 2 like any wrong arg shape, a configured project whose
// browse_port resolves empty is a plain 1 — the identifier was fine, the
// config has nothing to browse — and so is a port nobody is listening on. An
// explicit configured project with a served port needs NO checkout: 0.
func TestBrowseExitCodes(t *testing.T) {
	cases := map[string]struct {
		args []string
		want int
	}{
		"unknown workspace":      {args: []string{"browse", "NOPE-9"}, want: 3},
		"unknown project":        {args: []string{"browse", "A-1", "nope"}, want: 3},
		"nothing checked out":    {args: []string{"browse", "A-1"}, want: 2},
		"empty browse_port":      {args: []string{"browse", "A-1", "noport"}, want: 1},
		"nothing listening":      {args: []string{"browse", "A-1", "dead"}, want: 1},
		"explicit project, port": {args: []string{"browse", "A-1", "app"}, want: 0},
		"no args":                {args: []string{"browse"}, want: 2},
		"too many args":          {args: []string{"browse", "A-1", "app", "extra"}, want: 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			browseRoot(t)
			if got := exitCodeFor(t, tc.args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d (spec §9)", tc.args, got, tc.want)
			}
		})
	}
}

// TestBrowsePrintsURLWithoutOpener pins the SSH-friendly output: no xdg-open
// on PATH means the URL alone on stdout — substituted port, no "opening"
// prefix, nothing spawned.
func TestBrowsePrintsURLWithoutOpener(t *testing.T) {
	port := browseRoot(t)
	root := Root()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"browse", "A-1", "app"})
	if err := root.Execute(); err != nil {
		t.Fatalf("browse: %v", err)
	}
	if got, want := out.String(), "http://localhost:"+port+"\n"; got != want {
		t.Errorf("browse output = %q, want %q", got, want)
	}
}

// TestBrowseNothingCheckedOutNamesCheckout pins the defaulting refusal's
// content: with nothing checked out there are no candidates to list, so the
// message must point at the command that changes that.
func TestBrowseNothingCheckedOutNamesCheckout(t *testing.T) {
	browseRoot(t)
	err := runCLI(t, "browse", "A-1")
	if err == nil {
		t.Fatal("browse with nothing checked out succeeded, want a usage error")
	}
	if !strings.Contains(err.Error(), "checkout") {
		t.Errorf("error %q does not point at checkout", err)
	}
}
