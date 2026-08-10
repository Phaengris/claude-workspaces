package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// TestExtractSessionFlags pins the hand-written extractor: only the region
// BEFORE the first literal `--` is scanned for the tool's session flags; that
// one `--` is dropped and everything after it passes through verbatim —
// including strings spelled exactly like our flags, and any LATER `--`.
func TestExtractSessionFlags(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		skipPerms bool
		noResume  bool
		rest      []string
	}{
		{"empty", nil, true, false, nil},
		{"plain args untouched", []string{"hello", "world"}, true, false, []string{"hello", "world"}},
		{"-S short form", []string{"-S"}, false, false, nil},
		{"--claude-no-skip-permissions long form", []string{"--claude-no-skip-permissions"}, false, false, nil},
		{"-R short form", []string{"-R"}, true, true, nil},
		{"--claude-no-resume long form", []string{"--claude-no-resume"}, true, true, nil},
		{"both flags mixed with args", []string{"-S", "foo", "-R", "bar"}, false, true, []string{"foo", "bar"}},
		{"claude's own flags pass through", []string{"-p", "--model", "opus"}, true, false, []string{"-p", "--model", "opus"}},
		{"after -- our flags are claude's", []string{"--", "-S", "-R"}, true, false, []string{"-S", "-R"}},
		{"only the first -- is dropped", []string{"-S", "--", "x", "--", "-R"}, false, false, []string{"x", "--", "-R"}},
		{"flags before --, look-alikes after", []string{"-R", "--", "--claude-no-skip-permissions"}, true, true, []string{"--claude-no-skip-permissions"}},
		{"lone --", []string{"--"}, true, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			skipPerms, noResume, rest := extractSessionFlags(tc.args)
			if skipPerms != tc.skipPerms || noResume != tc.noResume {
				t.Errorf("flags = (skipPerms=%v, noResume=%v), want (%v, %v)",
					skipPerms, noResume, tc.skipPerms, tc.noResume)
			}
			if !slices.Equal(rest, tc.rest) {
				t.Errorf("rest = %q, want %q", rest, tc.rest)
			}
		})
	}
}

// TestBuildClaudeArgv enumerates the injection matrix (spec §8, the decided
// row). Left side: --dangerously-skip-permissions is injected iff the policy
// stands (no -S) AND the user took no permission stance of their own. Right
// side: --continue is injected iff history exists AND nothing suppresses it
// (-R, print mode, or any user resume flag). Injected flags always precede the
// user's args.
func TestBuildClaudeArgv(t *testing.T) {
	cases := []struct {
		name      string
		rest      []string
		skipPerms bool
		noResume  bool
		history   bool
		want      []string
	}{
		// --- permissions half -------------------------------------------------
		{"fresh default injects skip-permissions",
			nil, true, false, false,
			[]string{"--dangerously-skip-permissions"}},
		{"-S policy suppresses injection",
			nil, false, false, false,
			nil},
		{"user --permission-mode suppresses injection",
			[]string{"--permission-mode", "plan"}, true, false, false,
			[]string{"--permission-mode", "plan"}},
		{"user --permission-mode=plan suppresses injection",
			[]string{"--permission-mode=plan"}, true, false, false,
			[]string{"--permission-mode=plan"}},
		{"user --dangerously-skip-permissions is never doubled",
			[]string{"--dangerously-skip-permissions"}, true, false, false,
			[]string{"--dangerously-skip-permissions"}},
		// --- resume half -------------------------------------------------------
		{"history injects --continue",
			nil, false, false, true,
			[]string{"--continue"}},
		{"no history, no --continue",
			nil, false, false, false,
			nil},
		{"-R policy suppresses --continue",
			nil, false, true, true,
			nil},
		{"-p print mode suppresses --continue",
			[]string{"-p", "hi"}, false, false, true,
			[]string{"-p", "hi"}},
		{"--print suppresses --continue",
			[]string{"--print"}, false, false, true,
			[]string{"--print"}},
		{"user -c is never doubled",
			[]string{"-c"}, false, false, true,
			[]string{"-c"}},
		{"user --continue is never doubled",
			[]string{"--continue"}, false, false, true,
			[]string{"--continue"}},
		{"user -r suppresses --continue",
			[]string{"-r"}, false, false, true,
			[]string{"-r"}},
		{"user --resume suppresses --continue",
			[]string{"--resume", "abc"}, false, false, true,
			[]string{"--resume", "abc"}},
		{"user --resume=abc suppresses --continue",
			[]string{"--resume=abc"}, false, false, true,
			[]string{"--resume=abc"}},
		{"user --from-pr suppresses --continue",
			[]string{"--from-pr", "12"}, false, false, true,
			[]string{"--from-pr", "12"}},
		// --- both halves together ---------------------------------------------
		{"both injected, in front of user args",
			[]string{"--model", "opus"}, true, false, true,
			[]string{"--dangerously-skip-permissions", "--continue", "--model", "opus"}},
		{"perm injected while user resumes explicitly",
			[]string{"-r"}, true, false, true,
			[]string{"--dangerously-skip-permissions", "-r"}},
		{"continue injected while user sets permission mode",
			[]string{"--permission-mode", "plan"}, true, false, true,
			[]string{"--continue", "--permission-mode", "plan"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildClaudeArgv(tc.rest, tc.skipPerms, tc.noResume, tc.history)
			if !slices.Equal(got, tc.want) {
				t.Errorf("argv = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEncodeClaudeProjectDir pins the memory-dir encoding: EVERY byte outside
// [A-Za-z0-9] becomes '-', not just '/' and '.'. Verified empirically on this
// machine — see the encodeClaudeProjectDir doc comment; the underscore case is
// the one that matters daily, because this tool's own workspace dirs are named
// <task>_<slug>.
func TestEncodeClaudeProjectDir(t *testing.T) {
	cases := map[string]string{
		"/home/cat/dev/claude-workspaces": "-home-cat-dev-claude-workspaces",
		// The empirical evidence: a real underscore-bearing dir on this machine.
		"/home/cat/claude-workspaces/PATADM_patternima-admin-panel": "-home-cat-claude-workspaces-PATADM-patternima-admin-panel",
		// Dots are non-alphanumeric, so they encode like everything else.
		"/srv/my.app/T-1_fix.v2": "-srv-my-app-T-1-fix-v2",
	}
	for dir, want := range cases {
		if got := encodeClaudeProjectDir(dir); got != want {
			t.Errorf("encodeClaudeProjectDir(%q) = %q, want %q", dir, got, want)
		}
	}
}

// TestHasConversation pins the probe against fixture HOMEs: only a *.jsonl
// ENTRY inside the encoded project dir counts as history.
func TestHasConversation(t *testing.T) {
	wsDir := "/root/T-1_fix.things"
	enc := "-root-T-1-fix-things"
	projDir := func(t *testing.T, home string) string {
		t.Helper()
		dir := filepath.Join(home, ".claude", "projects", enc)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("no projects dir at all", func(t *testing.T) {
		if hasConversation(t.TempDir(), wsDir) {
			t.Error("empty HOME must have no conversation")
		}
	})
	t.Run("encoded dir exists but is empty", func(t *testing.T) {
		home := t.TempDir()
		projDir(t, home)
		if hasConversation(home, wsDir) {
			t.Error("empty project dir must have no conversation")
		}
	})
	t.Run("non-jsonl entries do not count", func(t *testing.T) {
		home := t.TempDir()
		dir := projDir(t, home)
		if err := os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if hasConversation(home, wsDir) {
			t.Error("a .txt file must not count as a conversation")
		}
	})
	t.Run("a jsonl file is a conversation", func(t *testing.T) {
		home := t.TempDir()
		dir := projDir(t, home)
		if err := os.WriteFile(filepath.Join(dir, "abc.jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !hasConversation(home, wsDir) {
			t.Error("a .jsonl file must count as a conversation")
		}
	})
	// --- the symlink fallback (M5 debt row) ---------------------------------
	// Claude Code records history under the encoding of the path IT was told to
	// run in, and that is whatever cwd it resolved — so a workspace root reached
	// through a symlink (a moved/linked claude-workspaces dir, the common case
	// on this developer's machines) records under the RESOLVED path while the
	// registry, and hence ws.Dir, keeps the as-written one. Probing only one of
	// the two silently loses --continue; the probe tries both.
	symlinkFixture := func(t *testing.T) (home, asWritten, resolved string) {
		t.Helper()
		home = t.TempDir()
		base := t.TempDir()
		real := filepath.Join(base, "real", "T-1_x")
		if err := os.MkdirAll(real, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "link")); err != nil {
			t.Fatal(err)
		}
		asWritten = filepath.Join(base, "link", "T-1_x")
		resolved, err := filepath.EvalSymlinks(asWritten)
		if err != nil {
			t.Fatal(err)
		}
		if resolved == asWritten {
			t.Fatalf("fixture is not exercising the fallback: %q resolves to itself", asWritten)
		}
		return home, asWritten, resolved
	}
	writeConv := func(t *testing.T, home, dir string) {
		t.Helper()
		enc := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(dir))
		if err := os.MkdirAll(enc, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(enc, "abc.jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("history under the symlink-RESOLVED path counts", func(t *testing.T) {
		home, asWritten, resolved := symlinkFixture(t)
		writeConv(t, home, resolved)
		if !hasConversation(home, asWritten) {
			t.Error("history recorded under the resolved path must be found for the as-written dir")
		}
	})
	t.Run("history under the AS-WRITTEN path still counts", func(t *testing.T) {
		home, asWritten, _ := symlinkFixture(t)
		writeConv(t, home, asWritten)
		if !hasConversation(home, asWritten) {
			t.Error("the as-written encoding must remain the first thing probed")
		}
	})
	t.Run("neither encoding: still no history", func(t *testing.T) {
		home, asWritten, _ := symlinkFixture(t)
		if hasConversation(home, asWritten) {
			t.Error("a symlinked dir with no history anywhere must have no conversation")
		}
	})

	t.Run("a DIFFERENT workspace's history does not count", func(t *testing.T) {
		home := t.TempDir()
		other := filepath.Join(home, ".claude", "projects", "-root-T-2-other")
		if err := os.MkdirAll(other, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other, "abc.jsonl"), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if hasConversation(home, wsDir) {
			t.Error("another workspace's jsonl must not count")
		}
	})
}

// TestSessionEnv pins the session-env rule (M4 plan, Global Constraints): the
// session inherits THIS process's env (already SanitizeSelf'd at startup) and
// the workspace overlay — resolved global env plus the runtime vars (WORKSPACE
// and the numbered values) — wins per key. This is deliberately NOT the
// curated allowlist: Claude is the operator's tool.
func TestSessionEnv(t *testing.T) {
	t.Setenv("SESSION_PARENT_VAR", "inherited")
	t.Setenv("DB_NAME", "parent-loses")   // collides with the overlay
	t.Setenv("PWD", "/wherever/launched") // the launcher's cwd: stale for the child
	cfg := &config.Config{
		Values: map[string]config.Value{"PORT": {Start: 5000, PerWorkspace: 10}},
		Env:    map[string]string{"DB_NAME": "app_${WORKSPACE}_dev"},
	}
	ws := wsp.Workspace{Dir: "/x/T-9_y", Alloc: alloc.Allocation{TaskID: "T-9", Index: 1}}

	got := map[string]string{}
	for _, kv := range sessionEnv(cfg, ws) {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	want := map[string]string{
		"SESSION_PARENT_VAR": "inherited",   // inherited env reaches the session
		"DB_NAME":            "app_T-9_dev", // overlay wins over the parent
		"WORKSPACE":          "T-9",         // runtime identity var exported
		"PORT0":              "5010",        // value vars exported (index 1 × 10)
		"PORT9":              "5019",        // ... the whole block
		"PWD":                "/x/T-9_y",    // ws.Dir, not the launcher's cwd
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("sessionEnv[%s] = %q, want %q", k, got[k], v)
		}
	}
}

// claudeFixture builds a root with one allocated workspace T-1, an isolated
// HOME (so the history probe never sees the developer's real ~/.claude), and a
// PATH holding exactly the given shim script as `claude` — PATH discipline: a
// real claude binary must never be reachable from these tests. Empty script ⇒
// empty PATH (the missing-binary case).
func claudeFixture(t *testing.T, shim string) (root string) {
	t.Helper()
	root = fixtureRoot(t, map[string]string{"config.yml": validConfig})
	wsDir := filepath.Join(root, "T-1_x")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	regJSON := fmt.Sprintf(`{%q: {"index": 0, "task_id": "T-1", "description": "x", "created_at": "2026-08-10T00:00:00Z", "adopted": false}}`, wsDir)
	if err := os.WriteFile(filepath.Join(root, ".allocations.json"), []byte(regJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	if shim == "" {
		t.Setenv("PATH", t.TempDir()) // an empty dir: nothing to find
		return root
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return root
}

// TestClaudeExitCodePropagation pins the exit-code contract end-to-end: the
// child's code becomes OURS, verbatim, through xerr.Exit → ExitCode. txtar
// scripts can only assert zero/non-zero, so the exact-code pin lives here.
func TestClaudeExitCodePropagation(t *testing.T) {
	claudeFixture(t, "#!/bin/sh\nexit 7\n")
	if got := exitCodeFor(t, "claude", "T-1"); got != 7 {
		t.Errorf("exit code = %d, want 7 (the shim's own)", got)
	}
}

// TestClaudeMissingBinary: no claude on PATH is a plain error (exit 1) whose
// message names the problem — not a panic, not a cryptic exec error.
func TestClaudeMissingBinary(t *testing.T) {
	claudeFixture(t, "")
	err := runCLI(t, "claude", "T-1")
	if err == nil {
		t.Fatal("expected an error with no claude on PATH")
	}
	if got := xerr.ExitCode(classifyUsageError(err)); got != 1 {
		t.Errorf("exit code = %d, want 1", got)
	}
	if !strings.Contains(err.Error(), "claude") || !strings.Contains(err.Error(), "PATH") {
		t.Errorf("error %q should name the claude binary and PATH", err)
	}
}

// TestClaudeSessionSurvivesSIGINT pins the signal wrap (Task 1 review ruling):
// while the child owns the terminal, the shell delivers Ctrl-C to the whole
// foreground group — parent included — and a parent that dies mid-session
// leaves the tty to a still-running claude. The shim aims SIGINT at ITS parent
// (this test binary, which is the `workspace` process here) and then keeps
// running; a session that returns cleanly proves the parent absorbed it.
//
// HAZARD, deliberate: if the wrap regresses, the signal takes its default
// action and kills the TEST BINARY — a loud, unmistakable failure rather than a
// silent one. That is why the pin is here and not in a txtar script.
func TestClaudeSessionSurvivesSIGINT(t *testing.T) {
	// Builtins only: claudeFixture's PATH holds the shim and nothing else, so
	// the post-signal dwell (long enough for delivery, short enough not to
	// matter) is a shell loop rather than `sleep`.
	claudeFixture(t, "#!/bin/sh\nkill -INT $PPID\ni=0\nwhile [ $i -lt 200000 ]; do i=$((i+1)); done\nexit 0\n")
	if err := runCLI(t, "claude", "T-1"); err != nil {
		t.Fatalf("session must survive a Ctrl-C aimed at the parent: %v", err)
	}
}

// TestClaudeUsageAndResolve pins the DisableFlagParsing edges: with cobra's
// parser off, no Args validator runs and no flag error can fire, so OUR arg
// check must classify a missing workspace as usage (exit 2), and an unknown
// workspace stays exit 3 via Resolve. -h/--help in the workspace slot still
// prints help (exit 0) — the one flag users WILL type there.
func TestClaudeUsageAndResolve(t *testing.T) {
	claudeFixture(t, "#!/bin/sh\nexit 0\n")
	if got := exitCodeFor(t, "claude"); got != 2 {
		t.Errorf("bare `claude` exit code = %d, want 2 (usage)", got)
	}
	if got := exitCodeFor(t, "claude", "nope"); got != 3 {
		t.Errorf("unknown workspace exit code = %d, want 3", got)
	}
	if got := exitCodeFor(t, "claude", "--help"); got != 0 {
		t.Errorf("`claude --help` exit code = %d, want 0 (help text)", got)
	}
}
