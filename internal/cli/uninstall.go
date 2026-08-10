package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/config"
)

// newUninstallCmd builds `workspace uninstall`: remove EXACTLY the paths the
// install manifest lists, then the manifest itself, and say what was
// deliberately left behind. The manifest is the whole contract — see the
// package comment in install.go — and this command's one hard rule is that it
// never removes a path the manifest does not list.
//
// Failure policy is continue-and-join, like gc's: uninstall is a batch, and
// one entry's problem must not leave the seven others installed. An entry
// that is already gone is FINE (half an uninstall re-run to completion); an
// entry that fails removal is reported and skipped; an entry that fails the
// refusal check below is reported and NOT removed. The manifest is removed at
// the end regardless — every listed entry has by then been either removed,
// found missing, or refused-with-error, and a manifest kept around after that
// would promise a second run something different from what the first one
// already said.
//
// THE REFUSAL CHECK (refuseRemoval): the manifest lives in the user's home
// and could have been edited by anyone or anything, so entries no honest
// install could have written are refused rather than executed: a relative
// path (meaningless here — install records absolute paths only, and a
// relative one would resolve against whatever the cwd happens to be), the
// filesystem root, $HOME itself, and the workspaces root itself (user data —
// the one directory uninstall exists to preserve). Everything else is
// removed as listed, wherever it points: the manifest is install's own
// record, and second-guessing it further would make uninstall leave litter
// whenever a layout detail changes between versions.
func newUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove everything `workspace install` recorded, keep all user data",
		Args:  usageArgs(cobra.NoArgs),
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

			paths, err := readManifest(lay.manifest)
			if errors.Is(err, fs.ErrNotExist) {
				// Not an error: there is nothing to undo, and saying so at
				// exit 0 keeps `uninstall` idempotent.
				fmt.Fprintln(out, "nothing installed")
				return nil
			}
			if err != nil {
				// A manifest that exists but cannot be parsed is kept on
				// disk: it is the only record of what was installed, and
				// deleting it here would trade a readable error for
				// permanent litter.
				return err
			}

			var errs []error
			for _, p := range paths {
				if reason := refuseRemoval(p, home, root); reason != "" {
					errs = append(errs, fmt.Errorf("refusing to remove %q: %s", p, reason))
					continue
				}
				switch err := os.Remove(p); {
				case errors.Is(err, fs.ErrNotExist):
					// Already gone — deleted by hand, or a prior partial run.
				case err != nil:
					errs = append(errs, err)
				default:
					fmt.Fprintf(out, "removed %s\n", p)
				}
			}
			if err := os.Remove(lay.manifest); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			} else {
				fmt.Fprintf(out, "removed %s\n", lay.manifest)
			}

			// What uninstall deliberately leaves: the user's data, and the
			// one edit install asked the USER to make — the hook entry in
			// settings.json is not ours to remove any more than it was ours
			// to add.
			fmt.Fprintf(out, `
left untouched:
  %s (your workspaces and config.yml)
  the SessionStart hook entry in %s (remove it by hand)
`, root, filepath.Join(home, ".claude", "settings.json"))

			return errors.Join(errs...)
		},
	}
}

// refuseRemoval reports why a manifest entry must NOT be removed — the empty
// string means it may be. The rules are listed in newUninstallCmd's doc
// comment; paths are Clean-ed first so dressed-up spellings of the protected
// targets ("/x/..", "$HOME/.") cannot slip past the comparison.
func refuseRemoval(path, home, root string) string {
	if !filepath.IsAbs(path) {
		return "not an absolute path"
	}
	switch clean := filepath.Clean(path); clean {
	case "/":
		return "the filesystem root"
	case filepath.Clean(home):
		return "the home directory itself"
	case filepath.Clean(root):
		return "the workspaces root itself"
	}
	return ""
}
