package config

import (
	"strings"
	"testing"
)

func TestValidateCollectsAllErrors(t *testing.T) {
	cfg := &Config{
		Values: map[string]Value{"PORT": {Start: 0, PerWorkspace: 0}},
		Projects: map[string]*Project{
			"a": {Depends: StringList{"ghost"}},
		},
	}
	err := cfg.validate()
	if err == nil {
		t.Fatal("want validation errors")
	}
	for _, want := range []string{
		`project "a": repo is required`,
		`project "a": depends on unknown project "ghost"`,
		`value "PORT": start must be positive`,
		`value "PORT": per_workspace must be at least 1`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should contain %q, got:\n%v", want, err)
		}
	}
}

// TestValidateProjectPath pins the containment half of config validation: a
// project's `path` decides where inside the workspace dir its worktree lives,
// and destroy/new force-remove that location — so a path that can point
// OUTSIDE the workspace (absolute, or any `..` component, or "the workspace
// itself" after cleaning) must be rejected at load time, before any command
// can act on it.
func TestValidateProjectPath(t *testing.T) {
	cases := map[string]struct {
		path string
		ok   bool
	}{
		"empty defaults to key": {path: "", ok: true},
		"simple relative":       {path: "sub", ok: true},
		"nested relative":       {path: "sub/dir", ok: true},
		"dot segment inside":    {path: "sub/./dir", ok: true},
		"absolute":              {path: "/etc/shared", ok: false},
		"parent escape":         {path: "../shared", ok: false},
		"interior escape":       {path: "a/../../b", ok: false},
		// `a/../b` cleans to a safe `b`, but the rule is literal — any `..`
		// component is rejected, so nobody has to reason about Clean semantics.
		"harmless dotdot": {path: "a/../b", ok: false},
		"dot":             {path: ".", ok: false},
		"dot slash":       {path: "./", ok: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := &Config{Projects: map[string]*Project{"p": {Repo: "/r", Path: tc.path}}}
			err := cfg.validate()
			if tc.ok {
				if err != nil {
					t.Errorf("path %q rejected: %v", tc.path, err)
				}
				return
			}
			want := `project "p": path must be relative and must not escape the workspace (got "` + tc.path + `")`
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("path %q: want error containing %q, got %v", tc.path, want, err)
			}
		})
	}
}

func TestValidateDetectsDependencyCycle(t *testing.T) {
	cfg := &Config{Projects: map[string]*Project{
		"a": {Repo: "/r", Depends: StringList{"b"}},
		"b": {Repo: "/r", Depends: StringList{"a"}},
	}}
	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Errorf("want dependency cycle error, got %v", err)
	}
}

func TestCycleReportListsParticipants(t *testing.T) {
	// b<->c cycle with a depending on b: current Kahn implementation reports
	// downstream node "a" as involved too. This test PINS that behavior and
	// documents it — if you change the algorithm to report only true cycle
	// members, update this test and the doc comment together.
	cfg := &Config{Projects: map[string]*Project{
		"a": {Repo: "/r", Depends: StringList{"b"}},
		"b": {Repo: "/r", Depends: StringList{"c"}},
		"c": {Repo: "/r", Depends: StringList{"b"}},
	}}
	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "dependency cycle involving: [a b c]") {
		t.Errorf("want cycle error listing [a b c], got %v", err)
	}
}

func TestValidateOK(t *testing.T) {
	cfg := &Config{Projects: map[string]*Project{
		"a": {Repo: "/r", Depends: StringList{"b"}},
		"b": {Repo: "/r"},
	}}
	if err := cfg.validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}
