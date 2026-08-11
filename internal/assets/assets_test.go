package assets_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Phaengris/claude-workspaces/internal/assets"
	"github.com/Phaengris/claude-workspaces/internal/config"
	"github.com/Phaengris/claude-workspaces/internal/wsp"
)

// TestAccessorsCarryTheirContent is the embed round-trip: every accessor must
// return a non-empty file, and each file must still contain the marks that
// make it that file. The sentinels are the load-bearing lines — the skill's
// frontmatter name (Claude Code keys the skill on it), the hook's binary
// guard and workspace probe, the wrappers' `command workspace` passthrough
// (without it the wrapper recurses into itself) — so a rename or a rewrite
// that drops one fails here rather than at install time.
func TestAccessorsCarryTheirContent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		got       []byte
		sentinels []string
	}{
		{"Skill", assets.Skill(), []string{
			"name: claude-workspaces",
			"description:",
			"work on",
			"workspace launch",
			"WORKSPACE.md",
		}},
		{"SessionStartHook", assets.SessionStartHook(), []string{
			"#!/bin/sh",
			"command -v workspace",
			"workspace which",
			"workspace status",
			"exit 0",
		}},
		{"FishWrapper", assets.FishWrapper(), []string{
			"function workspace",
			"command workspace $argv",
			"cd ",
		}},
		{"BashWrapper", assets.BashWrapper(), []string{
			"workspace()",
			`command workspace "$@"`,
			"cd ",
		}},
		{"ConfigStub", assets.ConfigStub(), []string{
			"values:",
			"per_workspace:",
			"env_allow:",
			"templates:",
			"projects:",
			"browse_port:",
			"instructions:",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.got) == 0 {
				t.Fatalf("%s() is empty — embed pattern missing the file?", tc.name)
			}
			for _, s := range tc.sentinels {
				if !bytes.Contains(tc.got, []byte(s)) {
					t.Errorf("%s() must contain %q", tc.name, s)
				}
			}
		})
	}
}

// TestAccessorsReturnCopies pins the defensive copy. The installer writes
// these bytes to disk and a future caller might template or trim them in
// place; the embedded data is process-global, so a mutation that reached it
// would corrupt every later read in the same process.
func TestAccessorsReturnCopies(t *testing.T) {
	first := assets.Skill()
	if len(first) == 0 {
		t.Fatal("Skill() is empty")
	}
	want := first[0]
	first[0] = 'X'
	if got := assets.Skill()[0]; got != want {
		t.Errorf("Skill() aliases the embedded bytes: after mutation got %q, want %q", got, want)
	}
}

// TestConfigStubIsLoadableAsShipped is THE pin on the config stub: `workspace
// install` writes it to <root>/config.yml, and the very next command loads
// that file. Strict decoding means a stray key or a mis-indented example
// makes a fresh install fail on every command — so the stub is checked
// through the real config.Load, not a YAML parse.
//
// The shipped stub must also be QUIET: the commented-out reference block is
// documentation, and the live part declares no project, so nothing points at
// a repo that does not exist on the new machine (which `doctor` would report
// and `checkout` would fail on).
func TestConfigStubIsLoadableAsShipped(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.yml"), assets.ConfigStub(), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("config.Load on the shipped stub: %v", err)
	}
	if len(cfg.Projects) != 0 {
		t.Errorf("the shipped stub must declare no live project, got %d", len(cfg.Projects))
	}
	// The live half is not empty either: the PORT block is what makes
	// ${PORT0} in the commented examples mean something.
	if v, ok := cfg.Values["PORT"]; !ok {
		t.Error("the shipped stub must declare a live PORT value block")
	} else if v.Start <= 0 || v.PerWorkspace < 1 {
		t.Errorf("PORT block must be valid, got %+v", v)
	}
}

// Fences delimiting the stub's uncomment-me example blocks. They exist so
// this test can check the DOCUMENTATION and not just the live skeleton: a
// commented example is the first thing a user uncomments, and a typo'd key in
// it (or a key renamed in the schema and not here) would be a strict-decode
// error in their face on the next command.
const (
	exampleBegin = "# >>> example"
	exampleEnd   = "# <<< example"
)

// uncommentExamples splits the stub's fenced example blocks out and strips
// their comment prefix, yielding the config document a user would get by
// uncommenting all of them. Each block carries its own top-level key
// (env, env_allow, templates, projects), so concatenating them in file order
// is a single valid document — the live `values:`/`projects:` lines are
// deliberately left out, since `projects:` appears in both.
func uncommentExamples(t *testing.T, stub []byte) string {
	t.Helper()
	var (
		out    []string
		inside bool
		blocks int
	)
	for _, line := range strings.Split(string(stub), "\n") {
		switch strings.TrimRight(line, " ") {
		case exampleBegin:
			if inside {
				t.Fatalf("nested %q fence in config_stub.yml", exampleBegin)
			}
			inside, blocks = true, blocks+1
			continue
		case exampleEnd:
			if !inside {
				t.Fatalf("unmatched %q fence in config_stub.yml", exampleEnd)
			}
			inside = false
			continue
		}
		if !inside {
			continue
		}
		switch {
		case line == "#":
			out = append(out, "")
		case strings.HasPrefix(line, "# "):
			out = append(out, strings.TrimPrefix(line, "# "))
		default:
			t.Fatalf("line inside an example block is not a comment: %q", line)
		}
	}
	if inside {
		t.Fatalf("unclosed %q fence in config_stub.yml", exampleBegin)
	}
	if blocks < 4 {
		t.Fatalf("expected the stub to fence env, env_allow, templates and projects examples, found %d blocks", blocks)
	}
	return strings.Join(out, "\n") + "\n"
}

// TestConfigStubExamplesAreValidWhenUncommented loads the stub's example
// blocks as a real config. Beyond "it parses", it pins that the example
// exercises the whole schema the stub claims to document: the template is
// instantiated, `depends` resolves, and both start-entry forms decode.
func TestConfigStubExamplesAreValidWhenUncommented(t *testing.T) {
	doc := uncommentExamples(t, assets.ConfigStub())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.yml"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("the stub's example blocks must be a valid config, got: %v\n--- document ---\n%s", err, doc)
	}
	app := cfg.Projects["my-app"]
	if app == nil {
		t.Fatal("example must define project my-app")
	}
	var bare, daemons int
	for _, e := range app.Start {
		if e.Name == "" {
			bare++
		} else {
			daemons++
		}
	}
	if bare == 0 || daemons == 0 {
		t.Errorf("my-app must demonstrate both start forms, got %d bare and %d daemons", bare, daemons)
	}
	if len(app.Depends) == 0 || app.BrowsePort == "" || app.Instructions == "" || len(app.EnvAllow) == 0 {
		t.Errorf("my-app must demonstrate depends/browse_port/instructions/env_allow, got %+v", app)
	}
	// The templated project proves load-time ${PARAM} substitution while
	// runtime tokens survive: repo is expanded, ${PORT0} is not.
	acme := cfg.Projects["acme"]
	if acme == nil || !strings.HasSuffix(acme.Repo, "/clients/acme") {
		t.Errorf("templated project acme must resolve its repo param, got %+v", acme)
	} else if acme.BrowsePort != "${PORT0}" {
		t.Errorf("runtime tokens must survive template expansion, got browse_port %q", acme.BrowsePort)
	}
	// ${WORKSPACE} is the TASK ID, not the workspace directory name
	// (wsp.RuntimeVars binds it to taskID) — and the stub's token list says so,
	// because a user keying a database or container name on it would otherwise
	// get a different string than documented. Resolved through the real
	// substitution path, on the example's own DATABASE_URL.
	env := wsp.ResolvedEnv(cfg, "TASK-123", "my-app", 1)
	if got, want := env["DATABASE_URL"], "postgres:///my_app_TASK-123"; got != want {
		t.Errorf("${WORKSPACE} must resolve to the task id: DATABASE_URL = %q, want %q", got, want)
	}
}

// writeAsset drops an embedded script into dir and returns its path.
func writeAsset(t *testing.T, dir, name string, data []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// shimBin writes an executable `workspace` shim into a fresh bin dir and
// returns the dir, for use as the hook's entire PATH. The shim is the tool as
// far as the hook can tell: it answers `which` and `status` and nothing else,
// so the test drives the hook's branches without a built binary, a root, or a
// registry.
func shimBin(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	writeAsset(t, dir, "workspace", []byte(script), 0o755)
	return dir
}

// runHook runs the embedded hook with PATH set to exactly binDir (plus the
// dirs sh itself needs for `command -v`), returning stdout, stderr and the
// exit code.
func runHook(t *testing.T, binDir string) (stdout, stderr string, code int) {
	t.Helper()
	hook := writeAsset(t, t.TempDir(), "session-start.sh", assets.SessionStartHook(), 0o755)
	cmd := exec.Command("sh", hook)
	// /usr/bin:/bin stay on PATH so the shim script's own `printf`/`exit`
	// resolve; the shim dir goes FIRST so its `workspace` wins over any real
	// installation on the developer's machine.
	cmd.Env = []string{"PATH=" + binDir + ":/usr/bin:/bin", "HOME=" + t.TempDir()}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if err != nil {
		if !errors.As(err, &ee) {
			t.Fatalf("running hook: %v", err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

// TestHookInsideWorkspacePrintsContext pins the whole context block, byte for
// byte. This text lands in the model's session context at SessionStart, so
// its shape is a contract with the reader, not an implementation detail: the
// workspace name is stated once in prose, `status` output is passed through
// verbatim (the hook never reformats it), and the closing lines point at
// WORKSPACE.md and the lifecycle commands.
func TestHookInsideWorkspacePrintsContext(t *testing.T) {
	bin := shimBin(t, `#!/bin/sh
case "$1" in
which)  printf 'A-1_x\n' ;;
status) printf 'workspace: %s\nindex: 0\n' "$2" ;;
*)      exit 1 ;;
esac
`)
	stdout, stderr, code := runHook(t, bin)
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (a hook must never fail a session)", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	want := `# claude-workspaces

This session is inside workspace A-1_x.

workspace: A-1_x
index: 0

WORKSPACE.md holds the task, the allocated values and per-project instructions.
Manage this workspace with: workspace status|up|down|logs|exec A-1_x
`
	if stdout != want {
		t.Errorf("hook stdout mismatch\n--- got ---\n%s\n--- want ---\n%s", stdout, want)
	}
}

// TestHookOutsideWorkspaceIsSilent pins the other branch: `which` exits 3
// ("not inside a workspace") and the hook must add nothing at all — no
// heading, no "not in a workspace" note. Session context is expensive; a hook
// with nothing to say says nothing.
//
// The shim also writes to stderr, as the real binary does on exit 3, to pin
// that the hook swallows it: a diagnostic at every session start outside a
// workspace would be pure noise.
func TestHookOutsideWorkspaceIsSilent(t *testing.T) {
	bin := shimBin(t, `#!/bin/sh
if [ "$1" = which ]; then
	echo 'workspace: not inside a workspace' >&2
	exit 3
fi
exit 1
`)
	stdout, stderr, code := runHook(t, bin)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty outside a workspace", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty (the probe's diagnostic must be swallowed)", stderr)
	}
}

// TestHookWithoutBinaryIsSilent pins the guard: the hook is a file in
// ~/.local/share and the settings.json entry that runs it outlives any
// uninstall, so "workspace is not on PATH" is a normal state, not an error.
func TestHookWithoutBinaryIsSilent(t *testing.T) {
	stdout, stderr, code := runHook(t, t.TempDir()) // empty bin dir: no `workspace`
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("output must be empty without the binary, got stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestHookEmptyProbeIsSilent covers the third silent case: `workspace which`
// succeeds but prints nothing. That cannot happen today, but the hook's next
// step interpolates the name into a `status` call, and an empty name there
// would print a bare, confusing block — so the guard is pinned.
func TestHookEmptyProbeIsSilent(t *testing.T) {
	bin := shimBin(t, "#!/bin/sh\nexit 0\n")
	stdout, _, code := runHook(t, bin)
	if code != 0 || stdout != "" {
		t.Errorf("empty probe output must yield silence, got stdout=%q code=%d", stdout, code)
	}
}

// lookShell reports the path to a shell, skipping the test when the host does
// not have it. The wrappers are shell code: the only honest test is running
// them, and the suite must still pass on a machine without fish (CI images
// rarely ship it) — so those tests skip loudly rather than assert nothing.
func lookShell(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed — skipping the %s wrapper test", name, name)
	}
	return path
}

// TestFishWrapperParses is the syntax check: `fish -n` (parse, do not
// execute). A syntax error in an autoloaded function file breaks the user's
// prompt on every new shell, so this is the highest-value cheap check.
// Skipped when fish is absent (see lookShell).
func TestFishWrapperParses(t *testing.T) {
	fish := lookShell(t, "fish")
	path := writeAsset(t, t.TempDir(), "workspace.fish", assets.FishWrapper(), 0o644)
	out, err := exec.Command(fish, "-n", path).CombinedOutput()
	if err != nil {
		t.Errorf("fish -n %s: %v\n%s", path, err, out)
	}
}

// wrapperShim is the fake `workspace` binary both wrapper tests drive: `cd`
// prints a path or fails with 3 (not found), and with --json it prints a
// deliberate NON-path, standing in for the outputs the wrappers must not
// chdir to (a help page today, a JSON shape if `cd --json` ever grows one).
// Anything else is echoed with a passthrough marker, which is how the tests
// tell "the wrapper handed this to the binary" apart from "the wrapper acted
// on its own".
const wrapperShim = `#!/bin/sh
case "$1" in
cd)
	for a in "$@"; do
		if [ "$a" = --json ]; then printf 'JSON-NOT-A-PATH\n'; exit 0; fi
	done
	if [ "$2" != good ]; then echo 'workspace: no such workspace' >&2; exit 3; fi
	printf '%s\n' "$WSTARGET"
	;;
*)
	printf 'passthrough:%s\n' "$*"
	;;
esac
`

// checkWrapper runs the four cases every shell wrapper must satisfy. run
// executes one `workspace <args>` invocation in a shell that has sourced the
// wrapper, then prints the resulting working directory, and reports the
// combined output together with the exit code of the WRAPPER (not of the
// trailing pwd) — so one call tests where the shell ended up and what the
// caller would see as $?.
//
// The four cases are the whole contract: cd moves the shell; other
// subcommands reach the binary untouched; a failed cd neither moves the shell
// nor flattens the exit code (2 usage / 3 not found / 4 config are scripted
// against); and a cd form whose output is not a path (--help, or --json if it
// ever returns one) is passed through instead of being chdir-ed to.
func checkWrapper(t *testing.T, startDir, target string, run func(args string) (string, int)) {
	t.Helper()

	if out, code := run("cd good"); code != 0 || !strings.Contains(out, target) {
		t.Errorf("`workspace cd good` must chdir the shell to %q, got out=%q code=%d", target, out, code)
	}
	if out, code := run("status good"); code != 0 || !strings.Contains(out, "passthrough:status good") {
		t.Errorf("non-cd subcommands must pass through, got out=%q code=%d", out, code)
	} else if strings.Contains(out, target) {
		t.Errorf("a passthrough subcommand must not move the shell, got out=%q", out)
	}
	if out, code := run("cd bad"); code != 3 || strings.Contains(out, target) {
		t.Errorf("a failed `cd` must keep exit 3 and leave the shell in %q, got out=%q code=%d", startDir, out, code)
	}
	if out, code := run("cd good --json"); code != 0 || !strings.Contains(out, "JSON-NOT-A-PATH") || strings.Contains(out, target) {
		t.Errorf("`cd --json` must pass through unchdir-ed, got out=%q code=%d", out, code)
	}
}

// wrapperFixture lays out a start directory, a chdir target inside it and the
// shim on PATH, and returns the start dir, the target and the environment the
// shell must run with.
func wrapperFixture(t *testing.T) (startDir, target string, env []string) {
	t.Helper()
	startDir = t.TempDir()
	target = filepath.Join(startDir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := shimBin(t, wrapperShim)
	// os.Environ carries HOME and the like (fish wants them even with
	// --no-config); the appended PATH wins, os/exec keeping the last
	// assignment of a duplicated name.
	env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "WSTARGET="+target)
	return startDir, target, env
}

// runInShell is the shared body of the two behavior tests: run the shell with
// argv (which sources the wrapper file, invokes `workspace` once, remembers
// its status, prints the directory the shell ended up in and exits with the
// remembered status) and report the combined output and that status.
func runInShell(t *testing.T, shell string, argv []string, dir string, env []string) (string, int) {
	t.Helper()
	cmd := exec.Command(shell, argv...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if err != nil {
		if !errors.As(err, &ee) {
			t.Fatalf("running %s: %v\n%s", shell, err, out)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

// TestFishWrapperBehavior pins the fish function against checkWrapper's four
// cases. `--no-config` keeps the developer's own fish config (which may well
// define its own `workspace`) out of the test.
func TestFishWrapperBehavior(t *testing.T) {
	fish := lookShell(t, "fish")
	dir, target, env := wrapperFixture(t)
	wrapper := writeAsset(t, t.TempDir(), "workspace.fish", assets.FishWrapper(), 0o644)
	checkWrapper(t, dir, target, func(args string) (string, int) {
		script := "source " + wrapper + "\nworkspace " + args + "; set -l rc $status; pwd; exit $rc"
		return runInShell(t, fish, []string{"--no-config", "-c", script}, dir, env)
	})
}

// TestBashWrapperParses is `bash -n` on the sourced file — same reasoning as
// the fish syntax check: a syntax error would break every shell that sources
// it from an rc file. zsh is parsed too when present, since the file is
// documented as bash/zsh.
func TestBashWrapperParses(t *testing.T) {
	for _, sh := range []string{"bash", "zsh"} {
		t.Run(sh, func(t *testing.T) {
			shell := lookShell(t, sh)
			path := writeAsset(t, t.TempDir(), "workspace.bash", assets.BashWrapper(), 0o644)
			out, err := exec.Command(shell, "-n", path).CombinedOutput()
			if err != nil {
				t.Errorf("%s -n %s: %v\n%s", sh, path, err, out)
			}
		})
	}
}

// TestBashWrapperBehavior runs the same four cases against the bash/zsh
// function — the two wrappers are one contract written twice, so they are
// held to one test. zsh is included when installed: `local`, `command` and
// "$@" behave alike there, and the file claims to support it.
func TestBashWrapperBehavior(t *testing.T) {
	// Each shell's spelling of "read no startup files": a developer's rc file
	// may well define its own `workspace`, and the test must see ours.
	noRC := map[string][]string{
		"bash": {"--noprofile", "--norc"},
		"zsh":  {"-f"},
	}
	for _, sh := range []string{"bash", "zsh"} {
		t.Run(sh, func(t *testing.T) {
			shell := lookShell(t, sh)
			dir, target, env := wrapperFixture(t)
			wrapper := writeAsset(t, t.TempDir(), "workspace.bash", assets.BashWrapper(), 0o644)
			checkWrapper(t, dir, target, func(args string) (string, int) {
				script := ". " + wrapper + "\nworkspace " + args + "; rc=$?; pwd; exit $rc"
				argv := append(append([]string{}, noRC[sh]...), "-c", script)
				return runInShell(t, shell, argv, dir, env)
			})
		})
	}
}

// TestSkillFrontmatterIsFirst pins the skill's file shape: Claude Code reads
// the YAML frontmatter delimited by the FIRST line of the file, so a stray
// blank line or a title above it makes the skill invisible.
func TestSkillFrontmatterIsFirst(t *testing.T) {
	lines := strings.Split(string(assets.Skill()), "\n")
	if len(lines) < 4 || lines[0] != "---" {
		t.Fatalf("SKILL.md must open with a `---` frontmatter fence, got %q", lines[0])
	}
	var closed bool
	for _, l := range lines[1:] {
		if l == "---" {
			closed = true
			break
		}
		if strings.HasPrefix(l, "name:") || strings.HasPrefix(l, "description:") {
			continue
		}
		if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") {
			continue // folded description continuation
		}
		t.Errorf("unexpected frontmatter line %q — frontmatter carries name and description only", l)
	}
	if !closed {
		t.Error("SKILL.md frontmatter is not closed by a `---` line")
	}
}
