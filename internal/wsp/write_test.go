package wsp_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

// update regenerates the golden files instead of comparing against them:
//
//	go test ./internal/wsp -run Golden -update
//
// Review the diff it produces — the golden file IS the layout contract, so a
// regeneration is a deliberate change to what agents read, never a way to make
// a red test green.
var update = flag.Bool("update", false, "rewrite testdata golden files")

// checkGolden compares got against testdata/<name> byte for byte, or rewrites
// it under -update.
func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden (re-run with -update to create it): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("WORKSPACE.md mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// goldenWorkspace builds the fixture the golden file pins: two configured
// projects, only "app" checked out, values PORT{start 5000, 2 per workspace}
// at index 1 (so PORT0=5002, PORT1=5003), and instructions worth quoting
// verbatim.
func goldenWorkspace(t *testing.T) wsp.Workspace {
	t.Helper()
	root := t.TempDir()
	// The workspace name is the dir's base name, so pin it rather than
	// inheriting t.TempDir()'s random suffix.
	wsDir := filepath.Join(root, "T-1_add-widgets")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mkRepoAt(t, filepath.Join(wsDir, "app"), "T-1")
	return wsp.Workspace{Dir: wsDir, Alloc: alloc.Allocation{
		Index:       1,
		TaskID:      "T-1",
		Description: "Add widgets to the thing",
		CreatedAt:   "2026-08-09T12:00:00Z",
	}}
}

func TestWriteWorkspaceMDGolden(t *testing.T) {
	ws := goldenWorkspace(t)
	cfg := testCfg()
	cfg.Projects["app"].Instructions = "Run `bin/rails s` on ${PORT0}.\n\nAsk before touching the schema.\n"

	if err := wsp.WriteWorkspaceMD(cfg, ws); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(ws.Dir, "WORKSPACE.md"))
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "workspace_md.golden", got)

	// Contract assertions independent of layout: the golden could be
	// regenerated wrong, these cannot.
	s := string(got)
	for _, must := range []string{
		"T-1_add-widgets",          // workspace name
		"T-1",                      // task id
		"Add widgets to the thing", // description
		"PORT0=5002", "PORT1=5003", // values, sorted
		"branch: T-1",                     // per-project branch
		"Ask before touching the schema.", // instructions verbatim
	} {
		if !strings.Contains(s, must) {
			t.Errorf("WORKSPACE.md must contain %q", must)
		}
	}
	if strings.Contains(s, "www") {
		t.Error("web is not checked out; it must not get a section")
	}
	// Verbatim means verbatim: runtime tokens in instructions are NOT
	// substituted — the agent reads them next to the Values block, and the
	// config text is quoted as written.
	if !strings.Contains(s, "Run `bin/rails s` on ${PORT0}.") {
		t.Error("instructions must be copied verbatim, tokens and all")
	}
	if i, j := strings.Index(s, "PORT0="), strings.Index(s, "PORT1="); i > j {
		t.Error("values must be sorted")
	}
}

// TestWriteWorkspaceMDRegenerates: the file is rewritten wholesale, so a stale
// project section from an earlier generation cannot survive.
func TestWriteWorkspaceMDRegenerates(t *testing.T) {
	ws := goldenWorkspace(t)
	cfg := testCfg()
	path := filepath.Join(ws.Dir, "WORKSPACE.md")
	if err := os.WriteFile(path, []byte("stale garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wsp.WriteWorkspaceMD(cfg, ws); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "stale garbage") {
		t.Error("WORKSPACE.md must be regenerated wholesale")
	}
}

func TestEnsureClaudeMDCreates(t *testing.T) {
	ws := wsp.Workspace{Dir: t.TempDir()}
	if err := wsp.EnsureClaudeMD(ws); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(ws.Dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "WORKSPACE.md") {
		t.Errorf("CLAUDE.md must reference WORKSPACE.md, got %q", data)
	}
	if n := strings.Count(strings.TrimSuffix(string(data), "\n"), "\n"); n != 0 {
		t.Errorf("CLAUDE.md must be a single line, got %q", data)
	}
}

// TestEnsureClaudeMDPreservesExisting is the spec §5 promise: agent notes
// accumulated in CLAUDE.md survive every regeneration, byte for byte.
func TestEnsureClaudeMDPreservesExisting(t *testing.T) {
	ws := wsp.Workspace{Dir: t.TempDir()}
	path := filepath.Join(ws.Dir, "CLAUDE.md")
	custom := "See @WORKSPACE.md\n\n## Agent notes\n\n- the seeds live in db/seeds.rb\n"
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 { // idempotent: repeated calls change nothing
		if err := wsp.EnsureClaudeMD(ws); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != custom {
		t.Errorf("existing CLAUDE.md must be untouched\ngot  %q\nwant %q", data, custom)
	}
}

func TestWriteEnvFileSeeding(t *testing.T) {
	repo := t.TempDir()
	wsDir := t.TempDir()
	cfg := testCfg()
	cfg.Projects["app"].Repo = repo

	src := strings.Join([]string{
		"# a comment",
		"",
		"SECRET=abc",
		"DB_NAME=old",
		"malformed line without an equals sign",
		"  # indented comment",
		`QUOTED="keep the quotes"`,
		"DUP=first",
		"DUP=last",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	projDir := filepath.Join(wsDir, "app")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ws := wsp.Workspace{Dir: wsDir, Alloc: alloc.Allocation{Index: 1, TaskID: "T-1"}}
	if err := wsp.WriteEnvFile(cfg, ws, "app"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(projDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"DB_NAME=app_T-1_dev",                  // overlay wins over the source
		"DUP=last",                             // duplicate key: last wins
		`QUOTED="keep the quotes"`,             // value verbatim, quotes kept
		"RAILS_ENV=development",                // global env
		"SECRET=abc",                           // source-only key survives
		"SHARED=project",                       // project env beats global
		"URL=http://localhost:5002/${UNKNOWN}", // runtime substitution
		"",
	}, "\n")
	if string(data) != want {
		t.Errorf(".env mismatch\ngot  %q\nwant %q", data, want)
	}

	info, err := os.Stat(filepath.Join(projDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf(".env mode = %v, want 0600", info.Mode().Perm())
	}
}

// TestWriteEnvFileSeedingTightensMode: a pre-existing world-readable .env is
// re-secured, not left as found — the file holds secrets.
func TestWriteEnvFileSeedingTightensMode(t *testing.T) {
	wsDir := t.TempDir()
	cfg := testCfg()
	cfg.Projects["app"].Repo = t.TempDir()
	projDir := filepath.Join(wsDir, "app")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, ".env"), []byte("OLD=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := wsp.Workspace{Dir: wsDir, Alloc: alloc.Allocation{Index: 1, TaskID: "T-1"}}
	if err := wsp.WriteEnvFile(cfg, ws, "app"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(projDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf(".env mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(filepath.Join(projDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "OLD=1") {
		t.Error(".env must be rewritten wholesale, not appended to")
	}
}

func TestWriteEnvFileNoSource(t *testing.T) {
	wsDir := t.TempDir()
	cfg := testCfg()
	cfg.Projects["app"].Repo = filepath.Join(t.TempDir(), "does-not-exist")
	projDir := filepath.Join(wsDir, "app")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ws := wsp.Workspace{Dir: wsDir, Alloc: alloc.Allocation{Index: 1, TaskID: "T-1"}}
	if err := wsp.WriteEnvFile(cfg, ws, "app"); err != nil {
		t.Fatalf("a missing source .env is not an error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(projDir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"DB_NAME=app_T-1_dev",
		"RAILS_ENV=development",
		"SHARED=project",
		"URL=http://localhost:5002/${UNKNOWN}",
		"",
	}, "\n")
	if string(data) != want {
		t.Errorf(".env mismatch\ngot  %q\nwant %q", data, want)
	}
}

// TestWriteEnvFileHonorsProjectPath: the destination follows ProjectDir, so a
// `path:` override lands in the right worktree.
func TestWriteEnvFileHonorsProjectPath(t *testing.T) {
	wsDir := t.TempDir()
	cfg := testCfg()
	cfg.Projects["web"].Repo = t.TempDir()
	if err := os.MkdirAll(filepath.Join(wsDir, "www"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := wsp.Workspace{Dir: wsDir, Alloc: alloc.Allocation{Index: 0, TaskID: "T-2"}}
	if err := wsp.WriteEnvFile(cfg, ws, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, "www", ".env")); err != nil {
		t.Errorf(".env must land in the project's path override: %v", err)
	}
}
