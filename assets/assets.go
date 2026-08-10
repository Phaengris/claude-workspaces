// Package assets carries the files `workspace install` writes to disk — the
// Claude Code skill, the SessionStart hook, the shell wrappers and the config
// stub — compiled into the binary. An installed `workspace` therefore never
// references the repository it was built from (spec §9): copying the binary
// somewhere copies its assets with it.
//
// The package sits at the repository root, next to the asset files
// themselves, because a //go:embed pattern cannot reach outside its own
// directory — the embedding package must live where the files live. Nothing
// else belongs here: callers use internal/assets, which layers the typed,
// documented accessors on top of this FS.
package assets

import (
	"embed"
	"io/fs"
)

// Each asset is named explicitly instead of embedded by directory glob, so
// adding a file under assets/ is a deliberate act — name it here, give it an
// accessor and a test — rather than something the binary silently starts
// carrying.
//
//go:embed skill/SKILL.md hooks/session-start.sh shell/workspace.fish shell/workspace.bash config_stub.yml
var files embed.FS

// FS returns the embedded asset tree, rooted at the assets directory (so
// paths read as "skill/SKILL.md", "hooks/session-start.sh", …). It is a
// function rather than an exported variable so the tree cannot be reassigned.
func FS() fs.FS { return files }
