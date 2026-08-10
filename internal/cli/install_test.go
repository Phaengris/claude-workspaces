package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The txtar script (install.txtar) covers the whole install → re-install →
// uninstall lifecycle against a fake $HOME. The tests here pin the two pieces
// whose interesting cases a script cannot reach: the same-file binary guard
// (the script's `workspace` is the test binary, never the installed copy) and
// the hostile-manifest refusals (a script would have to construct a manifest
// naming "/" — exactly the file a test must never risk touching outside a
// unit test's full control).

// TestCopyBinary pins copyBinary's three answers: a fresh copy, an overwrite,
// and the same-file skip. The same-file case is the guard's reason to exist —
// `workspace install` run FROM ~/.local/bin/workspace would otherwise open
// its own running binary for truncation.
func TestCopyBinary(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("BINARY-V1"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("fresh copy", func(t *testing.T) {
		dst := filepath.Join(dir, "sub", "workspace")
		skipped, err := copyBinary(src, dst)
		if err != nil || skipped {
			t.Fatalf("copyBinary = skipped %v, err %v; want false, nil", skipped, err)
		}
		got, err := os.ReadFile(dst)
		if err != nil || string(got) != "BINARY-V1" {
			t.Fatalf("dst content = %q, %v; want BINARY-V1", got, err)
		}
		info, err := os.Stat(dst)
		if err != nil || info.Mode().Perm() != 0o755 {
			t.Fatalf("dst mode = %v, %v; want 0755", info.Mode(), err)
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		dst := filepath.Join(dir, "existing")
		if err := os.WriteFile(dst, []byte("OLD"), 0o644); err != nil {
			t.Fatal(err)
		}
		skipped, err := copyBinary(src, dst)
		if err != nil || skipped {
			t.Fatalf("copyBinary = skipped %v, err %v; want false, nil", skipped, err)
		}
		got, _ := os.ReadFile(dst)
		if string(got) != "BINARY-V1" {
			t.Fatalf("dst content = %q; want BINARY-V1 (idempotent overwrite)", got)
		}
	})

	t.Run("same file", func(t *testing.T) {
		skipped, err := copyBinary(src, src)
		if err != nil || !skipped {
			t.Fatalf("copyBinary(src, src) = skipped %v, err %v; want true, nil", skipped, err)
		}
		got, _ := os.ReadFile(src)
		if string(got) != "BINARY-V1" {
			t.Fatalf("src content = %q after same-file copy; want BINARY-V1 intact", got)
		}
	})

	t.Run("same file via symlink", func(t *testing.T) {
		link := filepath.Join(dir, "link")
		if err := os.Symlink(src, link); err != nil {
			t.Fatal(err)
		}
		skipped, err := copyBinary(src, link)
		if err != nil || !skipped {
			t.Fatalf("copyBinary(src, symlink-to-src) = skipped %v, err %v; want true, nil", skipped, err)
		}
	})
}

// TestManifestRoundTrip pins the manifest as a plain JSON array of paths that
// a re-install REPLACES (not merges): the second write's list is the whole
// truth, because uninstall removes exactly what it reads here.
func TestManifestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "install-manifest.json")
	if err := writeManifest(path, []string{"/a", "/b"}); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(path, []string{"/c"}); err != nil {
		t.Fatal(err)
	}
	got, err := readManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "/c" {
		t.Fatalf("readManifest = %v; want [/c] (rewrite replaces)", got)
	}
	if _, err := readManifest(filepath.Join(t.TempDir(), "absent.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("readManifest(absent) err = %v; want fs.ErrNotExist", err)
	}
}

// TestUninstallRefusesHostileManifest is the safety-line pin: uninstall
// removes EXACTLY what the manifest lists, except that it refuses entries no
// honest install could have written — a relative path, "/", $HOME itself,
// and the workspaces root or anything under it (config.yml is the pin here:
// exactly the user data a tampered manifest would aim at). Refusals are
// errors (exit non-zero), but every other entry is still processed, and with
// no removal FAILURES the manifest still goes — a refusal is policy, not
// environment, so keeping the record for refused rows alone would wedge
// uninstall forever.
func TestUninstallRefusesHostileManifest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "claude-workspaces")
	t.Setenv("CLAUDE_WORKSPACES_ROOT_DIR", root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	userCfg := filepath.Join(root, "config.yml")
	if err := os.WriteFile(userCfg, []byte("# user data"), 0o644); err != nil {
		t.Fatal(err)
	}

	legit := filepath.Join(home, ".local", "bin", "workspace")
	if err := os.MkdirAll(filepath.Dir(legit), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legit, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := filepath.Join(home, ".local", "share", "workspace", "install-manifest.json")
	hostile := []string{"/", home, root, userCfg, "relative/path", legit}
	if err := writeManifest(manifest, hostile); err != nil {
		t.Fatal(err)
	}

	err := runUninstall(t)
	if err == nil {
		t.Fatal("uninstall on a hostile manifest returned nil; want refusal errors")
	}
	for _, want := range []string{"refusing to remove", "\"/\"", "not an absolute path", "user data inside"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// The refused targets stand; the legitimate entry and the manifest do not.
	for _, kept := range []string{home, root, userCfg} {
		if _, statErr := os.Stat(kept); statErr != nil {
			t.Errorf("refused target %s: %v; want intact", kept, statErr)
		}
	}
	if _, statErr := os.Stat(legit); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("legit entry %s still present (err %v); want removed despite refusals", legit, statErr)
	}
	if _, statErr := os.Stat(manifest); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("manifest still present (err %v); want removed — refusal-only runs delete it", statErr)
	}
}

// TestUninstallKeepsManifestOnFailedRemoval pins the retry contract: an entry
// that FAILS removal (as opposed to being refused or already gone) keeps the
// manifest alive, rewritten to the survivors — the failed entry plus any
// refused ones — so that after the user fixes the environment a retry can
// finish the job instead of answering `nothing installed` over unrecorded
// litter.
func TestUninstallKeepsManifestOnFailedRemoval(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod cannot make removal fail")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, "claude-workspaces")
	t.Setenv("CLAUDE_WORKSPACES_ROOT_DIR", root)

	locked := filepath.Join(home, "locked")
	blocked := filepath.Join(locked, "artifact")
	removable := filepath.Join(home, ".local", "bin", "workspace")
	for _, f := range []string{blocked, removable} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A read-only parent makes unlinking blocked fail with EACCES.
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	manifest := filepath.Join(home, ".local", "share", "workspace", "install-manifest.json")
	if err := writeManifest(manifest, []string{"/", blocked, removable}); err != nil {
		t.Fatal(err)
	}

	if err := runUninstall(t); err == nil {
		t.Fatal("uninstall with a blocked entry returned nil; want an error")
	}
	if _, statErr := os.Stat(removable); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("removable entry still present (err %v); want removed despite the failure", statErr)
	}
	// The manifest survives, listing exactly the survivors in order: the
	// refused "/" and the failed entry — NOT the removed one.
	got, err := readManifest(manifest)
	if err != nil {
		t.Fatalf("manifest after failed removal: %v; want it kept and readable", err)
	}
	if len(got) != 2 || got[0] != "/" || got[1] != blocked {
		t.Fatalf("surviving manifest = %v; want [/ %s]", got, blocked)
	}

	// Fix the environment; the retry removes the blocked entry and — with no
	// failure left, only the refusal — deletes the manifest (still erroring
	// on the refused row).
	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runUninstall(t); err == nil {
		t.Fatal("retry returned nil; want the refusal error for /")
	}
	if _, statErr := os.Stat(blocked); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("blocked entry still present after retry (err %v); want removed", statErr)
	}
	if _, statErr := os.Stat(manifest); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("manifest still present after retry (err %v); want removed — nothing failed", statErr)
	}
}

// runUninstall executes `workspace uninstall` in-process against the
// env-configured fake HOME, output discarded, returning the command error.
func runUninstall(t *testing.T) error {
	t.Helper()
	cmd := Root()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"uninstall"})
	return cmd.Execute()
}
