package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Phaengris/chaban/v2/internal/xerr"
)

// sessionFixture builds a root with one allocated workspace T-1, an isolated
// HOME, SHELL=/bin/sh, and a PATH of an empty dir plus the system dirs: a real claude
// must never be reachable from these tests. agentYAML is appended to the
// config; "" means no agent: block (and so no fallback either).
func sessionFixture(t *testing.T, agentYAML string) (root string) {
	t.Helper()
	root = fixtureRoot(t, map[string]string{"config.yml": validConfig + agentYAML})
	wsDir := filepath.Join(root, "T-1_x")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	regJSON := fmt.Sprintf(`{%q: {"index": 0, "task_id": "T-1", "description": "x", "created_at": "2026-08-10T00:00:00Z", "adopted": false}}`, wsDir)
	if err := os.WriteFile(filepath.Join(root, ".allocations.json"), []byte(regJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/sh")
	// setSessionTitle renames the tmux window named by these; /usr/bin may hold
	// a real tmux, and the developer's window is not ours to rename.
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	// An empty dir first, then the system dirs: a login shell with a PATH
	// lacking them forks without end on some machines' /etc/profile.d. The
	// real claude lives elsewhere; refuse to run if it does not.
	t.Setenv("PATH", t.TempDir()+":/usr/bin:/bin")
	if p, err := exec.LookPath("claude"); err == nil {
		t.Skipf("claude found at %s in the system dirs; the no-claude cases would not be hermetic", p)
	}
	return root
}

// TestChatExitCodePropagation pins the exit-code contract end to end: the
// agent's code becomes ours, verbatim. txtar can only see non-zero.
func TestChatExitCodePropagation(t *testing.T) {
	sessionFixture(t, "agent:\n  start: exit 7\n")
	if got := exitCodeFor(t, "chat", "T-1"); got != 7 {
		t.Errorf("exit code = %d, want 7 (the agent's own)", got)
	}
}

func TestChatUsage(t *testing.T) {
	sessionFixture(t, "agent:\n  start: exit 0\n")
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"chat"}, 2},
		{[]string{"chat", "-S", "T-1"}, 2},
		{[]string{"chat", "T-1", "-S"}, 2},
		{[]string{"chat", "T-1", "--claude-no-resume"}, 2},
		{[]string{"claude", "T-1", "--model", "x"}, 2},
		{[]string{"chat", "nope"}, 3},
		{[]string{"chat", "--help"}, 0},
	} {
		if got := exitCodeFor(t, tc.args...); got != tc.want {
			t.Errorf("%q exit code = %d, want %d", tc.args, got, tc.want)
		}
	}
}

func TestChatNoAgentNoClaude(t *testing.T) {
	sessionFixture(t, "")
	if got := exitCodeFor(t, "chat", "T-1"); got != 4 {
		t.Errorf("exit code = %d, want 4 (config)", got)
	}
}

// TestChatCanResumeFailureStartsNothing: a can_resume that fails (neither 0
// nor 1) aborts with exit 1 and the agent never starts.
func TestChatCanResumeFailureStartsNothing(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	sessionFixture(t, fmt.Sprintf("agent:\n  start: touch %s\n  resume: touch %s\n  can_resume: exit 3\n", marker, marker))
	err := runCLI(t, "chat", "T-1")
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := xerr.ExitCode(classifyUsageError(err)); got != 1 {
		t.Errorf("exit code = %d, want 1", got)
	}
	if !strings.Contains(err.Error(), "can_resume failed") {
		t.Errorf("error %q should say can_resume failed", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("agent started despite the failed probe")
	}
}

// TestChatSessionSurvivesSIGINT pins the signal wrap: the agent aims SIGINT at
// this test binary (the `chaban` process here) and keeps running; a session
// that returns cleanly proves the parent absorbed it. HAZARD, deliberate: if
// the wrap regresses the signal kills the TEST BINARY, a loud failure. A shell
// sits between chaban and the agent, so $PPID would not be chaban: the pid
// travels in CHABAN_TEST_PID.
func TestChatSessionSurvivesSIGINT(t *testing.T) {
	sessionFixture(t, "agent:\n  start: \"kill -INT $CHABAN_TEST_PID; i=0; while [ $i -lt 200000 ]; do i=$((i+1)); done\"\n")
	t.Setenv("CHABAN_TEST_PID", strconv.Itoa(os.Getpid()))
	if err := runCLI(t, "chat", "T-1"); err != nil {
		t.Fatalf("session must survive a Ctrl-C aimed at the parent: %v", err)
	}
}

// TestLaunchTryNoAgentNoClaudeCreateNothing is Review Focus 5: with no agent:
// block and no claude on PATH, launch and try fail with exit 4 BEFORE creating
// anything: registry bytes and root listing are unchanged.
func TestLaunchTryNoAgentNoClaudeCreateNothing(t *testing.T) {
	root := sessionFixture(t, "")
	snapshot := func() (string, string) {
		reg, err := os.ReadFile(filepath.Join(root, ".allocations.json"))
		if err != nil {
			t.Fatal(err)
		}
		ents, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		return string(reg), strings.Join(names, "\n")
	}
	reg0, list0 := snapshot()
	for _, args := range [][]string{{"launch", "T-9", "desc"}, {"try", "some", "words"}} {
		if got := exitCodeFor(t, args...); got != 4 {
			t.Errorf("%q exit code = %d, want 4", args, got)
		}
		if reg, list := snapshot(); reg != reg0 || list != list0 {
			t.Errorf("%q changed the root:\nregistry %q -> %q\nlisting %q -> %q", args, reg0, reg, list0, list)
		}
	}
}
