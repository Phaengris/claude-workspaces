package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Phaengris/claude-workspaces/internal/alloc"
)

// siblingRoot builds a root holding projectConfig plus four allocations whose
// dirs exercise the prefix trap (A-1_x next to A-1_xtra) and nesting
// (N-1_outer/N-2_inner inside N-1_outer). The directories are created too, so a
// test can chdir into them; path-containment itself is pinned by TestWorkspaceAt
// against a synthetic registry, which needs no filesystem at all.
func siblingRoot(t *testing.T) string {
	t.Helper()
	root := fixtureRoot(t, map[string]string{"config.yml": projectConfig})
	dirs := map[string]string{
		"A-1_x":               `{"index": 0, "task_id": "A-1"}`,
		"A-1_xtra":            `{"index": 1, "task_id": "A-1b"}`,
		"N-1_outer":           `{"index": 2, "task_id": "N-1"}`,
		"N-1_outer/N-2_inner": `{"index": 3, "task_id": "N-2"}`,
	}
	reg := "{"
	first := true
	for sub, a := range dirs {
		if err := os.MkdirAll(filepath.Join(root, sub, "deep"), 0o755); err != nil {
			t.Fatal(err)
		}
		if !first {
			reg += ","
		}
		first = false
		reg += `"` + filepath.Join(root, sub) + `": ` + a
	}
	reg += "}"
	if err := os.WriteFile(filepath.Join(root, ".allocations.json"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestWhichCdExitCodes pins the codes txtar can only assert as "non-zero" (spec
// §9). The load-bearing ones: `which` outside a workspace is 3, so a script can
// branch on it exactly as it does on `status <unknown>`; `cd` with a good
// workspace and a bogus project is also 3 — spec §9 promises exit 3 for
// "workspace/project not found", and that covers the project half of the
// identifier just as it covers the workspace half.
func TestWhichCdExitCodes(t *testing.T) {
	t.Run("which inside a workspace", func(t *testing.T) {
		root := siblingRoot(t)
		t.Chdir(filepath.Join(root, "A-1_x", "deep"))
		if got := exitCodeFor(t, "which"); got != 0 {
			t.Errorf("which inside a workspace exit code = %d, want 0", got)
		}
	})
	t.Run("which outside any workspace", func(t *testing.T) {
		root := siblingRoot(t)
		t.Chdir(root) // the root itself is not a workspace
		if got := exitCodeFor(t, "which"); got != 3 {
			t.Errorf("which outside a workspace exit code = %d, want 3 (not found, spec §9)", got)
		}
	})

	cases := map[string]struct {
		args []string
		want int
	}{
		"cd by name":              {args: []string{"cd", "A-1_x"}, want: 0},
		"cd by task id":           {args: []string{"cd", "A-1"}, want: 0},
		"cd with project":         {args: []string{"cd", "A-1_x", "app"}, want: 0},
		"cd unknown workspace":    {args: []string{"cd", "NOPE"}, want: 3},
		"cd unconfigured project": {args: []string{"cd", "A-1_x", "nope"}, want: 3},
		// Resolution happens before project validation, so an unknown workspace
		// keeps its own code even when the project is bogus too.
		"cd unknown workspace and project": {args: []string{"cd", "nope", "nope"}, want: 3},
		// --json is accepted and ignored, not a usage error.
		"cd with json flag": {args: []string{"cd", "A-1_x", "--json"}, want: 0},
		// Arg-count violations are usage errors, via usageArgs.
		"which extra arg": {args: []string{"which", "extra"}, want: 2},
		"cd no args":      {args: []string{"cd"}, want: 2},
		"cd three args":   {args: []string{"cd", "a", "b", "c"}, want: 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := siblingRoot(t)
			t.Chdir(root)
			if got := exitCodeFor(t, tc.args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

// TestIsAncestorOrSame pins the path-containment contract, and the trap in
// particular: /ws/A-1 must NOT contain /ws/A-1b, even though it is a character
// prefix of it. Every case here is a bug a strings.HasPrefix implementation
// would ship.
func TestIsAncestorOrSame(t *testing.T) {
	cases := map[string]struct {
		dir, path string
		want      bool
	}{
		"same dir":                  {dir: "/ws/A-1", path: "/ws/A-1", want: true},
		"direct child":              {dir: "/ws/A-1", path: "/ws/A-1/app", want: true},
		"deep descendant":           {dir: "/ws/A-1", path: "/ws/A-1/app/src/pkg", want: true},
		"sibling with name prefix":  {dir: "/ws/A-1", path: "/ws/A-1b", want: false},
		"sibling prefix descendant": {dir: "/ws/A-1", path: "/ws/A-1b/app", want: false},
		"parent":                    {dir: "/ws/A-1", path: "/ws", want: false},
		"unrelated":                 {dir: "/ws/A-1", path: "/other/A-1", want: false},
		"root contains everything":  {dir: "/", path: "/ws/A-1", want: true},
		// Cleaning is Rel's job, so uncleaned inputs behave like their clean form.
		"trailing separator": {dir: "/ws/A-1/", path: "/ws/A-1/app", want: true},
		"dot segments":       {dir: "/ws/A-1", path: "/ws/A-1/./app/../lib", want: true},
		"escaping segments":  {dir: "/ws/A-1", path: "/ws/A-1/../A-1b", want: false},
		// Incomparable inputs are not ancestors: Rel errors out.
		"relative path": {dir: "/ws/A-1", path: "A-1/app", want: false},
		"relative dir":  {dir: "A-1", path: "/ws/A-1/app", want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isAncestorOrSame(tc.dir, tc.path); got != tc.want {
				t.Errorf("isAncestorOrSame(%q, %q) = %v, want %v", tc.dir, tc.path, got, tc.want)
			}
		})
	}
}

// TestWorkspaceAt pins which workspace a directory belongs to: the sibling trap
// resolves to itself, and nested workspaces resolve to the DEEPEST enclosing
// one — the registry order (map iteration) must not decide the answer.
func TestWorkspaceAt(t *testing.T) {
	reg := alloc.Registry{
		"/ws/A-1_x":               alloc.Allocation{Index: 0, TaskID: "A-1"},
		"/ws/A-1_xtra":            alloc.Allocation{Index: 1, TaskID: "A-1b"},
		"/ws/N-1_outer":           alloc.Allocation{Index: 2, TaskID: "N-1"},
		"/ws/N-1_outer/N-2_inner": alloc.Allocation{Index: 3, TaskID: "N-2"},
	}
	cases := map[string]struct {
		cwd  string
		want string // "" means no match
	}{
		"workspace dir itself":  {cwd: "/ws/A-1_x", want: "A-1_x"},
		"inside a workspace":    {cwd: "/ws/A-1_x/app/src", want: "A-1_x"},
		"sibling prefix name":   {cwd: "/ws/A-1_xtra", want: "A-1_xtra"},
		"inside sibling prefix": {cwd: "/ws/A-1_xtra/deep", want: "A-1_xtra"},
		// The trap's sharp edge: a plain directory whose path merely EXTENDS a
		// registry dir as a string. The two cases above are masked by
		// deepest-match (A-1_xtra is itself registered, so it wins anyway); here
		// nothing rescues a HasPrefix implementation, which would answer A-1_x.
		"unregistered prefix sibling":        {cwd: "/ws/A-1_x-scratch", want: ""},
		"inside unregistered prefix sibling": {cwd: "/ws/A-1_x-scratch/deep", want: ""},
		"outer of a nested pair":             {cwd: "/ws/N-1_outer/other", want: "N-1_outer"},
		"nested dir itself":                  {cwd: "/ws/N-1_outer/N-2_inner", want: "N-2_inner"},
		"inside the nested one":              {cwd: "/ws/N-1_outer/N-2_inner/sub", want: "N-2_inner"},
		"parent of all workspaces":           {cwd: "/ws", want: ""},
		"unrelated tree":                     {cwd: "/elsewhere/A-1_x", want: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ws, ok := workspaceAt(reg, tc.cwd)
			if tc.want == "" {
				if ok {
					t.Fatalf("workspaceAt(%q) = %q, want no match", tc.cwd, ws.Name())
				}
				return
			}
			if !ok {
				t.Fatalf("workspaceAt(%q) found nothing, want %q", tc.cwd, tc.want)
			}
			if ws.Name() != tc.want {
				t.Errorf("workspaceAt(%q) = %q, want %q", tc.cwd, ws.Name(), tc.want)
			}
		})
	}

	// The empty registry has no answer for anything — `which` turns that into
	// ErrNotFound rather than an empty print.
	if ws, ok := workspaceAt(alloc.Registry{}, "/ws/A-1_x"); ok {
		t.Errorf("workspaceAt on an empty registry = %q, want no match", ws.Name())
	}
	// Sanity: the value returned carries its allocation, not just the dir.
	ws, _ := workspaceAt(reg, "/ws/N-1_outer/N-2_inner/sub")
	if ws.Alloc.TaskID != "N-2" || ws.Dir != "/ws/N-1_outer/N-2_inner" {
		t.Errorf("workspaceAt returned %+v, want dir /ws/N-1_outer/N-2_inner task N-2", ws)
	}
}
