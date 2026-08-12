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
