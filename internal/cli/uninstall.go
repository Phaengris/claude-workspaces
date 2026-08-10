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
// one entry's problem must not leave the seven others installed. Every listed
// entry meets exactly one of FOUR outcomes:
//
//   - removed — printed, done;
//   - missing — already gone (deleted by hand, or a prior partial run):
//     fine, silently done;
//   - refused — the refusal check below said no: reported as an error,
//     never removed;
//   - FAILED — os.Remove errored (permissions, read-only filesystem, a
//     directory grown contents): reported as an error, still on disk.
//
// What happens to the manifest depends on which outcomes occurred. When any
// entry FAILED, the manifest is REWRITTEN with the survivors — the failed
// entries plus any refused ones — so that after the user fixes the
// environment (a chmod, a remount) the retry still knows what remains;
// deleting the record then would make the retry report `nothing installed`
// while the litter sits unrecorded forever, the exact trap the
// corrupt-manifest rule below exists to avoid. Only when NOTHING failed is
// the manifest removed — including refusal-only runs: a refusal is policy,
// not environment, so no retry will ever remove those entries, and a
// manifest kept for them alone would wedge uninstall permanently. Either
// way the errors join and the exit is non-zero.
//
// THE REFUSAL CHECK (refuseRemoval): the manifest lives in the user's home
// and could have been edited by anyone or anything, so entries no honest
// install could have written are refused rather than executed: a relative
// path (meaningless here — install records absolute paths only, and a
// relative one would resolve against whatever the cwd happens to be), the
// filesystem root, $HOME itself, and the workspaces root OR ANYTHING UNDER
// IT (user data — config.yml and the workspaces are the very things
// uninstall exists to preserve, and a tampered manifest must not reach
// them). Everything else is removed as listed, wherever it points: the
// manifest is install's own record, and second-guessing it further would
// make uninstall leave litter whenever a layout detail changes between
// versions.
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
			// Absolutize before the refusal compares: manifest entries are
			// absolute, and a RELATIVE $CLAUDE_WORKSPACES_ROOT_DIR would
			// otherwise never match one — silently disabling the root guard,
			// the worst possible failure mode for a safety check.
			if root, err = filepath.Abs(root); err != nil {
				return fmt.Errorf("resolving workspaces root: %w", err)
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

			// The four outcomes per entry — see the doc comment. survivors
			// keeps the manifest's own order (failed AND refused entries),
			// but only a FAILURE makes it worth writing back.
			var errs []error
			var survivors []string
			anyFailed := false
			for _, p := range paths {
				if reason := refuseRemoval(p, home, root); reason != "" {
					errs = append(errs, fmt.Errorf("refusing to remove %q: %s", p, reason))
					survivors = append(survivors, p)
					continue
				}
				switch err := os.Remove(p); {
				case errors.Is(err, fs.ErrNotExist):
					// Already gone — deleted by hand, or a prior partial run.
				case err != nil:
					errs = append(errs, err)
					survivors = append(survivors, p)
					anyFailed = true
				default:
					fmt.Fprintf(out, "removed %s\n", p)
				}
			}
			if anyFailed {
				// Keep the record of what is still on disk so a retry (after
				// the user fixes whatever blocked the removal) can finish
				// the job instead of answering `nothing installed`.
				if err := writeManifest(lay.manifest, survivors); err != nil {
					errs = append(errs, fmt.Errorf("recording unremoved entries: %w", err))
				} else {
					fmt.Fprintf(out, "kept %s (%d entries could not be removed — fix and re-run)\n",
						lay.manifest, len(survivors))
				}
			} else if err := os.Remove(lay.manifest); err != nil && !errors.Is(err, fs.ErrNotExist) {
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
  the now-empty tool directories under %s and %s (rmdir them if you like)
`, root, filepath.Join(home, ".claude", "settings.json"),
				filepath.Dir(lay.manifest), filepath.Dir(lay.skill))

			return errors.Join(errs...)
		},
	}
}

// refuseRemoval reports why a manifest entry must NOT be removed — the empty
// string means it may be. The rules are listed in newUninstallCmd's doc
// comment; paths are Clean-ed first so dressed-up spellings of the protected
// targets ("/x/..", "$HOME/.") cannot slip past the comparison. The
// workspaces-root rule is ancestor-or-same, not equality: install writes
// nothing inside the root except the stub it deliberately keeps out of the
// manifest, so an entry under the root can only come from tampering — and
// config.yml and the workspaces are exactly what it would be aimed at.
func refuseRemoval(path, home, root string) string {
	if !filepath.IsAbs(path) {
		return "not an absolute path"
	}
	clean := filepath.Clean(path)
	switch clean {
	case "/":
		return "the filesystem root"
	case filepath.Clean(home):
		return "the home directory itself"
	}
	// isAncestorOrSame is which.go's component-wise containment check —
	// reused, not re-derived, so the sibling-prefix bug documented there
	// cannot be reintroduced here. Lexical on purpose: the guard judges what
	// the manifest SAYS, and a lexical answer cannot be confused by a
	// filesystem arranged to lie about it.
	if isAncestorOrSame(root, clean) {
		return "the workspaces root or user data inside it"
	}
	return ""
}
