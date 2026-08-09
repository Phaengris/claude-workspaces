package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// TestDestroyExitCodes pins the codes destroy.txtar can only assert as
// "non-zero" (spec §9): an unresolvable workspace identifier is 3, an
// arg-count violation is a usage error, 2, via usageArgs, and a teardown
// failure is a plain error, 1.
func TestDestroyExitCodes(t *testing.T) {
	cases := map[string]struct {
		args []string
		want int
	}{
		"unknown workspace": {args: []string{"destroy", "NOPE"}, want: 3},
		"no args":           {args: []string{"destroy"}, want: 2},
		"two args":          {args: []string{"destroy", "a", "b"}, want: 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			registryRoot(t) // config with app/lib + allocation A-1 at <root>/A-1_x
			if got := exitCodeFor(t, tc.args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d (spec §9)", tc.args, got, tc.want)
			}
		})
	}

	// A failing teardown command aborts destroy with a plain error → exit 1.
	// destroy.txtar can only assert "non-zero" for it, so the code is pinned
	// here. Teardown only runs for projects git reports as checked out, hence
	// the real (if empty) repo inside the workspace dir. The message is
	// asserted alongside the code because the removal phase of this same
	// fixture would ALSO exit 1 (the configured repo is not a real one) —
	// without the marker the pin would pass for the wrong reason.
	t.Run("teardown failure", func(t *testing.T) {
		root := fixtureRoot(t, map[string]string{"config.yml": teardownFailConfig})
		gitInit(t, filepath.Join(root, "A-1_x", "app"))
		writeRegistry(t, root, filepath.Join(root, "A-1_x"), false)
		t.Setenv("SHELL", "/bin/sh") // proc.Run honours $SHELL; keep it hermetic
		err := runCLI(t, "destroy", "A-1")
		if got := xerr.ExitCode(classifyUsageError(err)); got != 1 {
			t.Errorf("destroy with failing teardown exit code = %d, want 1 (spec §9)", got)
		}
		if want := `project "app": command failed: teardown-boom`; err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q (the teardown's own failure)", err, want)
		}
	})
}

// teardownFailConfig's only project fails its teardown, marking stderr so the
// resulting error is distinguishable from any later phase's.
const teardownFailConfig = `projects:
  app:
    repo: /tmp/app-src
    teardown:
      - "echo teardown-boom >&2; false"
`

// writeRegistry writes a one-entry registry into root whose allocation A-1
// lives at dir. dir is a parameter precisely because the containment test
// needs it to point somewhere the tool would never have created; adopted is
// one because the containment gate exempts adopted workspaces, which
// legitimately live outside the root.
func writeRegistry(t *testing.T, root, dir string, adopted bool) {
	t.Helper()
	reg := fmt.Sprintf(`{%q: {"index": 0, "task_id": "A-1", "description": "x", `+
		`"created_at": "2026-08-01T09:00:00Z", "adopted": %t}}`, dir, adopted)
	if err := os.WriteFile(filepath.Join(root, ".allocations.json"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitInit makes dir a work tree, which is all wsp.ProjectStates asks (it
// calls `git rev-parse --is-inside-work-tree`). No commit, so no identity
// config is needed and the host's gitconfig cannot affect the answer.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-b", "main", dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", dir, err, out)
	}
}

// teardownMarkerRoot builds a root whose single project "app" records that its
// teardown ran by creating marker. A marker file is the only honest way to ask
// "did teardown run?" — teardown commands are arbitrary user code spawned in a
// shell, so their side effects, not the tool's output, are the evidence.
func teardownMarkerRoot(t *testing.T, marker string) string {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh") // proc.Run honours $SHELL; keep it hermetic
	cfg := fmt.Sprintf(`projects:
  app:
    repo: /tmp/app-src
    teardown:
      - "echo ran > %s"
`, marker)
	return fixtureRoot(t, map[string]string{"config.yml": cfg})
}

// TestDestroyRefusesWorkspaceDirOutsideRoot pins the gate in front of
// os.RemoveAll(ws.Dir). ws.Dir is a raw registry KEY — a hand-edited or
// corrupted .allocations.json can name any directory on the machine, and the
// per-worktree gate never fires for a workspace with nothing checked out, so
// without this check `destroy` is an arbitrary recursive delete. The fixture
// is exactly that shape: an allocation pointing at a SIBLING of the
// workspaces root.
//
// The gate must fire BEFORE the teardown loop, not just before RemoveAll.
// Teardown is not a read-only preamble: it spawns the user's own commands with
// cwd and ${WORKSPACE} substitution derived from THE SAME unvalidated registry
// entry, so running it first means acting on the corrupted path before the
// corruption is detected. The marker file below is what pins the ordering —
// asserting the refusal alone would pass with teardown already run.
func TestDestroyRefusesWorkspaceDirOutsideRoot(t *testing.T) {
	outside := t.TempDir() // sibling of the root below, not under it
	canary := filepath.Join(outside, "precious")
	if err := os.WriteFile(canary, []byte("do not delete"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outside, "teardown-ran")
	root := teardownMarkerRoot(t, marker)
	// A checked-out project is what makes teardown eligible to run at all.
	gitInit(t, filepath.Join(outside, "app"))
	writeRegistry(t, root, outside, false)

	err := runCLI(t, "destroy", "A-1")
	if err == nil {
		t.Fatal("destroy of a workspace dir outside the root succeeded; it must refuse")
	}
	// The refusal names BOTH paths: what it would have removed, and the root
	// it is not inside — enough to see the registry is the thing to fix.
	for _, want := range []string{"not strictly inside", outside, root} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	// The gate came first: not one teardown command was spawned.
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("teardown ran before the containment gate: the gate must precede the teardown loop, not merely the removal")
	}
	// Nothing removed: the dir, its contents, and the allocation all survive.
	if _, statErr := os.Stat(canary); statErr != nil {
		t.Errorf("%s was removed despite the refusal: %v", canary, statErr)
	}
	regBytes, readErr := os.ReadFile(filepath.Join(root, ".allocations.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(regBytes), "A-1") {
		t.Errorf("allocation was released despite the refusal: %s", regBytes)
	}
}

// TestDestroyAdoptedOutsideRootStillTearsDown is the other half of the gate's
// contract, and the reason it cannot simply be "reject anything outside the
// root": an ADOPTED workspace is a directory the tool did not create, so it
// legitimately lives anywhere. Teardown and release must still work for it;
// only the dir itself is left alone. Hoisting the gate must not have swept
// this case up.
func TestDestroyAdoptedOutsideRootStillTearsDown(t *testing.T) {
	outside := t.TempDir()
	marker := filepath.Join(outside, "teardown-ran")
	root := teardownMarkerRoot(t, marker)
	gitInit(t, filepath.Join(outside, "app"))
	writeRegistry(t, root, outside, true)

	if err := runCLI(t, "destroy", "A-1"); err != nil {
		t.Fatalf("destroy of an ADOPTED workspace outside the root: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("adopted workspace: teardown did not run (%v)", statErr)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Errorf("adopted workspace dir was removed; the tool never deletes dirs it did not create: %v", statErr)
	}
	regBytes, readErr := os.ReadFile(filepath.Join(root, ".allocations.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(regBytes), "A-1") {
		t.Errorf("adopted workspace: allocation was not released: %s", regBytes)
	}
}

// TestAssertInsideWorkspace pins the force-removal containment gate: nothing
// is ever removed unless the destination is STRICTLY inside the workspace dir,
// component-wise. The sibling-prefix trap (T-1_x vs T-1_xtra) and the
// same-dir case are the two ways a character-level check would go wrong.
func TestAssertInsideWorkspace(t *testing.T) {
	cases := map[string]struct {
		wsDir, dest string
		wantErr     bool
	}{
		"direct child":       {wsDir: "/root/T-1_x", dest: "/root/T-1_x/app", wantErr: false},
		"deep descendant":    {wsDir: "/root/T-1_x", dest: "/root/T-1_x/sub/dir", wantErr: false},
		"same dir":           {wsDir: "/root/T-1_x", dest: "/root/T-1_x", wantErr: true},
		"sibling prefix":     {wsDir: "/root/T-1_x", dest: "/root/T-1_xtra/app", wantErr: true},
		"parent":             {wsDir: "/root/T-1_x", dest: "/root", wantErr: true},
		"absolute escape":    {wsDir: "/root/T-1_x", dest: "/etc", wantErr: true},
		"escaping segments":  {wsDir: "/root/T-1_x", dest: "/root/T-1_x/../other", wantErr: true},
		"relative dest":      {wsDir: "/root/T-1_x", dest: "app", wantErr: true},
		"uncleaned interior": {wsDir: "/root/T-1_x", dest: "/root/T-1_x/./app", wantErr: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := assertInsideWorkspace(tc.wsDir, tc.dest)
			if (err != nil) != tc.wantErr {
				t.Errorf("assertInsideWorkspace(%q, %q) = %v, wantErr %v", tc.wsDir, tc.dest, err, tc.wantErr)
			}
		})
	}
}
