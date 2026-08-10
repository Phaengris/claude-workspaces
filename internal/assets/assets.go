// Package assets serves the embedded install-time files as bytes: the Claude
// Code skill, the SessionStart hook, the two shell wrappers and the config
// stub. It is the import point for `workspace install`; the files themselves
// live at the repository root under assets/ and are embedded there (a
// //go:embed pattern cannot reach a parent directory).
//
// The accessors are the whole API. Each one names where install puts the file
// and with what mode, because those two facts belong next to the content they
// describe, not only in the installer.
package assets

import (
	"fmt"
	"io/fs"

	embedded "git.internal/cat/claude-workspaces-go/assets"
)

// The files are read once, at package init: an embedded FS cannot change
// underfoot, and a per-call read would only add error paths that can never
// fire.
var (
	skill      = mustRead("skill/SKILL.md")
	hook       = mustRead("hooks/session-start.sh")
	fishWrap   = mustRead("shell/workspace.fish")
	bashWrap   = mustRead("shell/workspace.bash")
	configStub = mustRead("config_stub.yml")
)

// mustRead reads one embedded file. A failure means the //go:embed pattern
// and this path disagree — a build-time mistake with no runtime component, so
// it panics at init rather than handing the installer empty bytes and letting
// it write an empty file into the user's home. The embed round-trip test
// catches it long before a user could.
func mustRead(name string) []byte {
	data, err := fs.ReadFile(embedded.FS(), name)
	if err != nil {
		panic(fmt.Sprintf("internal/assets: embedded %s: %v", name, err))
	}
	return data
}

// clone returns a private copy. The embedded bytes are process-global and
// immutable by contract, so every accessor hands out a copy: a caller that
// templates or trims the content in place (a plausible thing for an installer
// to grow) cannot corrupt what the next caller reads.
func clone(b []byte) []byte { return append([]byte(nil), b...) }

// Skill returns the Claude Code skill document. Install target:
// ~/.claude/skills/claude-workspaces-go/SKILL.md, mode 0644. The directory
// name must match the skill's frontmatter `name`.
func Skill() []byte { return clone(skill) }

// SessionStartHook returns the SessionStart hook script (POSIX sh). Install
// target: ~/.local/share/workspace/hooks/session-start.sh, mode 0755 — it is
// executed, not sourced. Wiring it into ~/.claude/settings.json is the user's
// step; install only prints the snippet.
func SessionStartHook() []byte { return clone(hook) }

// FishWrapper returns the fish `workspace` function that makes `workspace cd`
// change the shell's directory. Install target:
// ~/.config/fish/functions/workspace.fish, mode 0644 — fish autoloads it from
// there, so no rc file is touched.
func FishWrapper() []byte { return clone(fishWrap) }

// BashWrapper returns the bash/zsh equivalent of FishWrapper. Install target:
// ~/.local/share/workspace/shell/workspace.bash, mode 0644. Bash and zsh have
// no autoload for functions, so the user sources it; install prints the line
// and never edits an rc file itself.
func BashWrapper() []byte { return clone(bashWrap) }

// ConfigStub returns the commented starter config. Install target:
// <root>/config.yml, mode 0644, ONLY when that file does not already exist —
// it is user data, and a re-install must never overwrite it. It is valid as
// shipped (config.Load accepts it) and declares no project.
func ConfigStub() []byte { return clone(configStub) }
