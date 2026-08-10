package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/assets"
	"git.internal/cat/claude-workspaces-go/internal/config"
)

// `workspace install` / `workspace uninstall` (spec §2, §9) are the one place
// this tool writes OUTSIDE the workspaces root, so the rules are stricter
// than anywhere else:
//
//   - Everything install writes is recorded, path by path, in a manifest
//     (~/.local/share/workspace/install-manifest.json), and uninstall removes
//     exactly what the manifest lists — nothing more (see uninstall.go for
//     the refusal rules that enforce it). The manifest deliberately EXCLUDES
//     the workspaces root and its config stub: those hold user data the
//     moment they exist, and uninstall must leave them standing.
//
//   - Nothing here edits ~/.claude/settings.json or any shell rc file
//     (plan's global constraint): the SessionStart hook and the bash/zsh
//     source lines are PRINTED for the user to apply. Invasive config edits
//     are not ours to make — and not ours to undo, which is why uninstall
//     only reminds about them.
//
//   - Re-install is idempotent: every tool-owned file is overwritten in
//     place (repairing whatever happened to it), the config stub is written
//     only if absent, and the manifest is rewritten last.

// installLayout names every path install touches under one $HOME, so the
// layout is declared once and install, uninstall and the tests all read the
// same map. The install targets and modes mirror the doc comments on the
// internal/assets accessors — those name the destination next to the content;
// this struct is where the destinations become code.
type installLayout struct {
	binary      string // ~/.local/bin/workspace, 0755 — copy of os.Executable()
	fishWrapper string // ~/.config/fish/functions/workspace.fish — the cd WRAPPER (autoloaded)
	fishComp    string // ~/.config/fish/completions/workspace.fish — the GENERATED completion script
	bashWrapper string // ~/.local/share/workspace/shell/workspace.bash — sourced by bash AND zsh
	bashComp    string // ~/.local/share/workspace/completions/workspace.bash — generated, sourced
	zshComp     string // ~/.local/share/workspace/completions/_workspace — generated, via $fpath
	skill       string // ~/.claude/skills/claude-workspaces-go/SKILL.md
	hook        string // ~/.local/share/workspace/hooks/session-start.sh, 0755 — executed
	manifest    string // ~/.local/share/workspace/install-manifest.json — the uninstall contract
}

// layoutFor derives the layout from a resolved home directory. Note the two
// same-named workspace.fish files: functions/ holds the wrapper fish
// autoloads, completions/ the generated completion script — swapping them
// would break both, silently.
func layoutFor(home string) installLayout {
	share := filepath.Join(home, ".local", "share", "workspace")
	fish := filepath.Join(home, ".config", "fish")
	return installLayout{
		binary:      filepath.Join(home, ".local", "bin", "workspace"),
		fishWrapper: filepath.Join(fish, "functions", "workspace.fish"),
		fishComp:    filepath.Join(fish, "completions", "workspace.fish"),
		bashWrapper: filepath.Join(share, "shell", "workspace.bash"),
		bashComp:    filepath.Join(share, "completions", "workspace.bash"),
		zshComp:     filepath.Join(share, "completions", "_workspace"),
		skill:       filepath.Join(home, ".claude", "skills", "claude-workspaces-go", "SKILL.md"),
		hook:        filepath.Join(share, "hooks", "session-start.sh"),
		manifest:    filepath.Join(share, "install-manifest.json"),
	}
}

// newInstallCmd builds `workspace install`: copy the running binary, write
// the shell integration, the completions, the Claude Code skill and the
// SessionStart hook into $HOME; create the workspaces root and its config
// stub; record every tool-owned path in the manifest; print what the user
// still has to do by hand.
func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the binary, shell integration, completions, skill and hook into $HOME",
		// No positionals, and no --root flag on purpose: the root comes from
		// $CLAUDE_WORKSPACES_ROOT_DIR / the default, exactly as it does for
		// every other command — install must set up the same universe the
		// rest of the tool will read.
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolving home dir: %w", err)
			}
			root, err := config.RootDir()
			if err != nil {
				return err
			}
			lay := layoutFor(home)
			out := cmd.OutOrStdout()

			// The binary: a copy of the running executable. The same-file
			// guard is what makes `~/.local/bin/workspace install` (the
			// installed binary re-running its own install) safe — copying a
			// file onto itself would truncate it under its own feet.
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locating the running binary: %w", err)
			}
			skipped, err := copyBinary(exe, lay.binary)
			if err != nil {
				return fmt.Errorf("installing binary: %w", err)
			}
			if skipped {
				fmt.Fprintf(out, "binary already installed at %s (running from it)\n", lay.binary)
			} else {
				fmt.Fprintf(out, "installed %s\n", lay.binary)
			}

			// The completion scripts are GENERATED from the live command
			// tree, not shipped as assets: they are cobra's client side of
			// the __complete protocol and must match the cobra this binary
			// links, not whatever was current when an asset was authored.
			var fishComp, bashComp, zshComp bytes.Buffer
			rootCmd := cmd.Root()
			if err := rootCmd.GenFishCompletion(&fishComp, true); err != nil {
				return fmt.Errorf("generating fish completions: %w", err)
			}
			if err := rootCmd.GenBashCompletionV2(&bashComp, true); err != nil {
				return fmt.Errorf("generating bash completions: %w", err)
			}
			if err := rootCmd.GenZshCompletion(&zshComp); err != nil {
				return fmt.Errorf("generating zsh completions: %w", err)
			}

			// Every remaining tool-owned file, written idempotently (atomic
			// replace, parents created). Only the hook is 0755: it is
			// executed by Claude Code; everything else is read or sourced.
			files := []struct {
				path string
				data []byte
				mode os.FileMode
			}{
				{lay.fishWrapper, assets.FishWrapper(), 0o644},
				{lay.fishComp, fishComp.Bytes(), 0o644},
				{lay.bashWrapper, assets.BashWrapper(), 0o644},
				{lay.bashComp, bashComp.Bytes(), 0o644},
				{lay.zshComp, zshComp.Bytes(), 0o644},
				{lay.skill, assets.Skill(), 0o644},
				{lay.hook, assets.SessionStartHook(), 0o755},
			}
			for _, f := range files {
				if err := writeFileAtomic(f.path, f.data, f.mode); err != nil {
					return fmt.Errorf("installing %s: %w", f.path, err)
				}
				fmt.Fprintf(out, "installed %s\n", f.path)
			}

			// The workspaces root and its config stub — user data from the
			// moment they exist, so: root created if missing, stub written
			// ONLY if absent (a re-install never overwrites user edits), and
			// neither recorded in the manifest below.
			if err := os.MkdirAll(root, 0o755); err != nil {
				return fmt.Errorf("creating workspaces root: %w", err)
			}
			cfgPath := filepath.Join(root, "config.yml")
			if _, err := os.Lstat(cfgPath); errors.Is(err, os.ErrNotExist) {
				if err := writeFileAtomic(cfgPath, assets.ConfigStub(), 0o644); err != nil {
					return fmt.Errorf("writing config stub: %w", err)
				}
				fmt.Fprintf(out, "created %s (starter config)\n", cfgPath)
			} else if err != nil {
				return fmt.Errorf("checking %s: %w", cfgPath, err)
			} else {
				fmt.Fprintf(out, "kept %s (exists, not overwritten)\n", cfgPath)
			}

			// The manifest goes LAST, atomically: it lists exactly the paths
			// written above, so a crash mid-install leaves either the old
			// manifest or the complete new one — never a list that promises
			// files that were not written.
			written := []string{
				lay.binary, lay.fishWrapper, lay.fishComp, lay.bashWrapper,
				lay.bashComp, lay.zshComp, lay.skill, lay.hook,
			}
			if err := writeManifest(lay.manifest, written); err != nil {
				return fmt.Errorf("writing manifest: %w", err)
			}
			fmt.Fprintf(out, "manifest: %s\n", lay.manifest)

			printInstallEpilogue(out, home, lay)
			return nil
		},
	}
}

// printInstallEpilogue prints the steps install deliberately leaves to the
// user: wiring the SessionStart hook into ~/.claude/settings.json and
// sourcing the wrapper + completions from bash/zsh rc files. These are the
// user's files; the tool prints, the user applies (and uninstall reminds).
// Fish needs no step — both its files sit in autoload directories.
func printInstallEpilogue(out io.Writer, home string, lay installLayout) {
	fmt.Fprintf(out, `
next steps — install edits no settings.json and no rc file; these are yours:

hook: add to %s:

  {
    "hooks": {
      "SessionStart": [
        { "hooks": [ { "type": "command", "command": %q } ] }
      ]
    }
  }

bash: add to ~/.bashrc:

  . %q
  . %q

zsh: add to ~/.zshrc:

  . %q
  fpath=(%s $fpath)
  autoload -U compinit && compinit

fish: nothing to add — the wrapper and completions autoload.

make sure %s is on your PATH.
`,
		filepath.Join(home, ".claude", "settings.json"), lay.hook,
		lay.bashWrapper, lay.bashComp,
		lay.bashWrapper, filepath.Dir(lay.zshComp),
		filepath.Dir(lay.binary))
}

// copyBinary copies the executable at src to dst (mode 0755, parents
// created), reporting skipped=true when src and dst are already the same
// file — the case where install runs FROM the installed binary, and a copy
// would truncate the very file being executed. Same-file is os.SameFile over
// Stat (which follows symlinks), so a dst symlinked to src is also skipped.
//
// The copy lands via temp-file-and-rename in dst's directory: an existing
// installed binary may be running right now, and overwriting it in place
// would fail with ETXTBSY — the rename swaps the directory entry instead and
// never touches the running inode.
func copyBinary(src, dst string) (skipped bool, err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	si, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	if di, err := os.Stat(dst); err == nil && os.SameFile(si, di) {
		return true, nil
	}
	in, err := os.Open(src)
	if err != nil {
		return false, err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".workspace-*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name()) // no-op after successful rename
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return false, err
	}
	if err := finishAtomic(tmp, dst, 0o755); err != nil {
		return false, err
	}
	return false, nil
}

// writeFileAtomic writes data to path via temp-file-and-rename (parents
// created): a reader — or a crash — never observes a half-written file, and
// an existing file keeps its old content until the new one is complete. The
// alloc registry's Save is the pattern; install pays the same fsync because
// these files are what the user's shell and Claude Code will load next.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".install-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	return finishAtomic(tmp, path, mode)
}

// finishAtomic is the shared tail of the two atomic writers: sync, close,
// chmod, rename. The chmod happens on the temp name BEFORE the rename so the
// file never exists at its final path with the temp file's default 0600.
func finishAtomic(tmp *os.File, path string, mode os.FileMode) error {
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// writeManifest records the installed paths as a JSON array — the exact
// contract uninstall executes. Atomic like everything else here; a rewrite
// REPLACES the list (re-install owns the whole truth, there is no merging).
func writeManifest(path string, paths []string) error {
	data, err := json.MarshalIndent(paths, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o644)
}

// readManifest reads the list writeManifest wrote. A missing file propagates
// as fs.ErrNotExist — uninstall's "nothing installed" case; a file that
// exists but does not parse is an error, NOT an empty list (an unreadable
// contract must stop uninstall, never silently shrink it).
func readManifest(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var paths []string
	if err := json.Unmarshal(data, &paths); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return paths, nil
}
