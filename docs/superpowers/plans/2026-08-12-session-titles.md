# Session Titles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `launch`/`claude` sessions automatically title the tmux window and
the terminal with the workspace name, and restore tmux auto-naming on exit.

**Architecture:** One small helper in `internal/cli` (`session_title.go`)
with pure, table-tested parts (rune truncation, OSC assembly, char-device
probe) and one orchestrator returning a restore closure; `runClaudeSession`
— the single runner both session commands share — calls it before spawning
and defers the restore. No new dependencies, no build tags, no stored state.

**Tech Stack:** Go stdlib only (os.Stat char-device probe, exec.Command for
tmux in argv form). testscript txtar with a tmux PATH shim.

**Spec:** `docs/superpowers/specs/2026-08-12-session-titles-design.md` — its
Decided-behaviors table binds every task.

## Global Constraints

- Clean-room: never consult Ruby v1 code. Spec + this repo only.
- Conventional commits; every commit ends with the two trailer lines used by
  every commit on this branch (copy from `git log -1 --format=%B`).
- `gofmt -l .` (prints nothing) and `go vet ./...` clean before every commit;
  full `go test ./...` green. TDD: failing test first. Mutation-check
  load-bearing pins and say so in the commit body.
- README changes in the SAME commit as the behavior they document.
- Best-effort and SILENT (spec row 6): no title/tmux failure may block, fail,
  or write to stderr. No new module, no build tags (README promises both).
- txtar: `$VAR` expands in UNQUOTED chunks only; a real `tmux` must never be
  invocable from tests (PATH shim prepended first, same discipline as
  git/claude).
- Work on branch `feat/session-titles` off master; push only at release.

---

### Task 1: The title helper — pure parts + orchestrator

**Files:**
- Create: `internal/cli/session_title.go`
- Create: `internal/cli/session_title_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `setSessionTitle(name string) (restore func())` — Task 2 calls it
  with `ws.Name()` and defers the restore. Also the unexported helpers
  `truncateRunes(s string, max int) string` and `oscTitle(name string) string`
  and the consts `tmuxTitleMax = 20`, `oscTitleMax = 40`.

- [ ] **Step 1: Write the failing table tests**

`internal/cli/session_title_test.go` (match the package's existing test
style — plain `testing`, table tests):

```go
package cli

import "testing"

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"shorter than max", "T-1_x", 20, "T-1_x"},
		{"exactly max", "12345", 5, "12345"},
		{"over max", "T-2_abcdefghijklmnopqrstuvwxyz", 20, "T-2_abcdefghijklmnop"},
		{"empty", "", 20, ""},
		{"multibyte clamped on runes", "日本語のワークスペース", 5, "日本語のワ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateRunes(tc.in, tc.max); got != tc.want {
				t.Fatalf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

func TestOSCTitle(t *testing.T) {
	// Exact bytes: OSC 0 (icon+window title), BEL-terminated — the most
	// widely understood title sequence.
	if got, want := oscTitle("T-1_x"), "\x1b]0;T-1_x\x07"; got != want {
		t.Fatalf("oscTitle = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestTruncateRunes|TestOSCTitle' -v`
Expected: FAIL to compile — `truncateRunes`/`oscTitle` undefined.

- [ ] **Step 3: Implement the helper**

`internal/cli/session_title.go`:

```go
package cli

import (
	"fmt"
	"os"
	"os/exec"
)

// Session titles (spec 2026-08-12): a session names its tmux window and its
// terminal after the workspace, so parallel workspaces are tellable apart in
// a status bar. Everything here is best-effort and SILENT — a title is a
// nicety, and no failure below (no tty, no tmux, a failed rename) may block,
// fail, or noise up the session it decorates (decided row 6).
const (
	// tmuxTitleMax is deliberately tighter than oscTitleMax: a tmux status
	// bar shows many windows side by side, a terminal title bar shows one.
	tmuxTitleMax = 20
	oscTitleMax  = 40
)

// truncateRunes clamps s to at most max runes, no ellipsis (decided row 2).
// Runes, not bytes: workspace names are ASCII slugs today, but a multi-byte
// description must not be cut mid-rune into invalid UTF-8.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// oscTitle is the OSC 0 (icon name + window title) escape, BEL-terminated —
// the variant every modern emulator understands. Inside tmux it sets the
// pane title; tmux's own set-titles option carries it to the outer terminal.
func oscTitle(name string) string {
	return "\x1b]0;" + name + "\x07"
}

// stdoutIsTerminal reports whether stdout is a character device — the
// stdlib-only tty probe (decided in the spec's Mechanics): pipes and files
// are not char devices, so redirected output stays byte-clean. The one false
// positive, /dev/null, discards the escapes it is sent.
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// setSessionTitle names the terminal and (inside tmux) the current tmux
// window after the workspace, returning the restore to run when the session
// ends. The restore is never nil; when nothing was renamed it is a no-op.
//
// Terminal: OSC 0 to stdout, only when stdout is a tty (decided row 3).
// tmux: `tmux rename-window` in argv form when $TMUX is set — NOT the \x1bk
// escape, which needs allow-rename (off by default in modern tmux) (row 4).
//
// The restore UNSETS the window's automatic-rename option rather than
// re-enabling it: rename-window is what set that option off, so unsetting it
// falls back to the user's global choice — auto-naming resumes on default
// configs, manual-name configs stay manual (decided row 5).
//
// The tmux child runs with THIS process's inherited env — it needs the real
// $TMUX socket; it is tool plumbing like gitx's git, not a user command, so
// the curated-env doctrine does not apply (decided row 7).
func setSessionTitle(name string) (restore func()) {
	if stdoutIsTerminal() {
		fmt.Fprint(os.Stdout, oscTitle(truncateRunes(name, oscTitleMax)))
	}
	if os.Getenv("TMUX") == "" {
		return func() {}
	}
	if exec.Command("tmux", "rename-window", truncateRunes(name, tmuxTitleMax)).Run() != nil {
		return func() {}
	}
	return func() {
		_ = exec.Command("tmux", "set-option", "-w", "-u", "automatic-rename").Run()
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/ -run 'TestTruncateRunes|TestOSCTitle' -v`
Expected: PASS. Then the full package: `go test ./internal/cli/` (nothing
calls `setSessionTitle` yet — the package must still compile and pass).

- [ ] **Step 5: Mutation-check the truncation pin**

Temporarily change `truncateRunes` to cut bytes (`s[:max]`); the multibyte
table case must fail. Restore. State the check in the commit body.

- [ ] **Step 6: gofmt, vet, full suite, commit**

```bash
cd /home/cat/dev/claude-workspaces
gofmt -l . && go vet ./... && go test ./...
git add internal/cli/session_title.go internal/cli/session_title_test.go
git commit -m "feat(cli): session-title helper — OSC + tmux rename with restore"
```

(Append the standard trailers.)

---

### Task 2: Wire into the session runner; pin end to end; README

**Files:**
- Modify: `internal/cli/claude.go` (`runClaudeSession`, ~line 121; its doc
  comment ~lines 100-120)
- Modify: `internal/cli/testdata/claude.txtar`
- Modify: `README.md` — Sessions section (`## Sessions:` heading) gains a
  short "Titles" paragraph; the v1-divergences appendix's dropped-commands
  bullet removes `title` from the dropped list and states the replacement
  (automatic on sessions, restore on exit); the Install section's build
  example version bumps `1.1.0` → `1.2.0`.

**Interfaces:**
- Consumes: `setSessionTitle(name string) (restore func())` (Task 1);
  `ws.Name()` (exists).

- [ ] **Step 1: Extend claude.txtar to pin the NEW behavior (failing first)**

In `internal/cli/testdata/claude.txtar`, after the existing blocks (keep them
byte-identical — no `$TMUX` is set there, so they also pin "no tmux → shim
never called" once the negative assert below lands):

```
# --- session titles: tmux window renamed, auto-name restored after ------------
# The `tmux` on PATH is a shim (PATH discipline as claude's: prepended first,
# a real tmux can never run) logging its argv. With $TMUX set, the session
# renames the window BEFORE claude runs and unsets automatic-rename AFTER it
# exits — rename-window is what turned that option off, so unsetting is the
# exact undo (spec 2026-08-12 rows 4-5).
chmod 0755 $WORK/bin/tmux
env TMUX_SHIM_LOG=$WORK/tmux.log
env TMUX=/tmp/fake-socket,1234,0
exec workspace claude T-1
grep -count=1 'rename-window T-1_x' $WORK/tmux.log
grep -count=1 'set-option -w -u automatic-rename' $WORK/tmux.log
exec sh -c 'test "$(sed -n 1p "$TMUX_SHIM_LOG")" = "rename-window T-1_x"'

# --- titles: the tmux window name is truncated to 20 runes --------------------
env TMUX_SHIM_LOG=$WORK/tmux-long.log
exec workspace new T-2 abcdefghijklmnopqrstuvwxyz app
exec workspace claude T-2
grep -count=1 'rename-window T-2_abcdefghijklmnop$' $WORK/tmux-long.log

# --- titles: outside tmux nothing is renamed; piped stdout stays clean --------
env TMUX_SHIM_LOG=$WORK/tmux-none.log
env TMUX=
exec workspace claude T-1
! exists $WORK/tmux-none.log
! stdout ']0;'
```

And the shim file at the end, next to `bin/claude`:

```
-- bin/tmux --
#!/bin/sh
echo "$@" >> "$TMUX_SHIM_LOG"
exit 0
```

Adapt to the file's actual fixture names: `T-1` exists with name `T-1_x`
(`workspace new T-1 x app` in the preamble); `T-2_abcdefghijklmnop` is the
first 20 runes of `T-2_abcdefghijklmnopqrstuvwxyz`. If the existing blocks'
`stdout` pins conflict with new output, they must NOT — no `$TMUX` and no
tty means the earlier blocks see zero new bytes; if one fails, that is a
bug in the implementation, not the test.

Run: `go test ./internal/cli/ -run 'TestScripts/claude' -v`
Expected: FAIL — no tmux calls are made yet (`! exists`/`grep` mismatches).

- [ ] **Step 2: Wire the runner**

In `runClaudeSession` (internal/cli/claude.go), after the argv/binary setup
and BEFORE `c.Run()` (concretely: right after the `c := exec.Command(...)`
block builds the command, before the signal.Notify block is also fine — the
title must be set before the child owns the terminal):

```go
	// Session titles (spec 2026-08-12): name the terminal and the tmux
	// window after the workspace for the duration of the session; the
	// deferred restore hands tmux its auto-naming back on every exit path,
	// error and absorbed-signal ones included.
	restore := setSessionTitle(ws.Name())
	defer restore()
```

Extend `runClaudeSession`'s doc comment with one sentence on titles (it
enumerates the runner's behaviors; the golden rule of this codebase is that
doc comments state contracts).

- [ ] **Step 3: Tests pass**

Run: `go test ./internal/cli/ -run 'TestScripts/claude' -v`, then the full
`go test ./...`.
Expected: PASS, including every pre-existing claude.txtar block unchanged
and launch.txtar untouched (launch shares runClaudeSession — the single call
site is the no-drift argument, no separate launch pin needed).

- [ ] **Step 4: Mutation-check the restore pin**

Temporarily drop the `defer restore()` line; the `set-option -w -u
automatic-rename` grep must fail. Restore. State the check in the commit
body.

- [ ] **Step 5: README (same commit)**

- Sessions section: add a short paragraph, e.g.:

  > **Titles.** A session names its terminal (OSC escape, when stdout is a
  > terminal) and, inside tmux, the current window (`tmux rename-window`,
  > first 20 characters of the workspace name) — and un-sets the window's
  > `automatic-rename` when the session ends, so tmux auto-naming resumes
  > exactly where it left off. Best-effort: no tmux, no tty, no problem.

- v1-divergences appendix: in the dropped-commands bullet, remove `title`
  and add: `title` returned in v1.2 as automatic behavior on `claude`/
  `launch` (tmux window + terminal title, restored on exit) — the first
  dropped command daily use proved missed.
- Install section: build example `-ldflags` version `1.1.0` → `1.2.0`.

- [ ] **Step 6: gofmt, vet, full suite, commit**

```bash
gofmt -l . && go vet ./... && go test ./...
git add internal/cli/ README.md
git commit -m "feat(session): title the tmux window and terminal for the session's duration"
```

(Append the standard trailers; cite spec rows 1-5 and both mutation checks
in the body.)

---

### Task 3: Release v1.2.0 (controller, after final review)

**Files:** none beyond the merge itself.

- [ ] **Step 1: Merge + release** (run by the controller once the final
  whole-branch review is clean):

```bash
git checkout master && git merge --no-ff feat/session-titles -m "Merge feat/session-titles: automatic session titles (v1.2.0)"
CGO_ENABLED=0 go build -ldflags "-X github.com/Phaengris/claude-workspaces/internal/cli.version=1.2.0" -o ./workspace ./cmd/workspace
./workspace --version   # prints: workspace version 1.2.0
./workspace install
git tag v1.2.0 && git push origin master v1.2.0
git branch -d feat/session-titles
```

(Trailers on the merge commit too.)

---

## Self-review notes (already applied)

- Spec coverage: rows 1-2 → Tasks 1-2; rows 3-5 → Task 1 code + Task 2 txtar
  pins; row 6 → helper design (silent, restore-never-nil) + no stderr pins
  implicit in existing stdout-exact blocks; row 7 → helper doc comment (tmux
  child inherits the tool's env by exec.Command default); row 8 → Task 3 +
  Task 2's README version bump.
- Type consistency: `setSessionTitle(name string) (restore func())` produced
  in Task 1, consumed by name in Task 2; consts named identically in both.
- The claude.txtar negative block (`env TMUX=` → `! exists`) doubles as the
  guard that the earlier, untouched blocks run tmux-free.
