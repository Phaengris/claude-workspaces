package wsp_test

import (
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

func testCfg() *config.Config {
	return &config.Config{
		Values: map[string]config.Value{"PORT": {Start: 5000, PerWorkspace: 2}},
		Env:    map[string]string{"RAILS_ENV": "development", "SHARED": "global"},
		Projects: map[string]*config.Project{
			"app": {Repo: "/r", Env: map[string]string{
				"DB_NAME": "app_${WORKSPACE}_dev",
				"URL":     "http://localhost:${PORT0}/${UNKNOWN}",
				"SHARED":  "project",
			}},
		},
	}
}

func TestSubst(t *testing.T) {
	vars := map[string]string{"WORKSPACE": "T-1", "PORT0": "5002"}
	cases := []struct{ in, want string }{
		{"app_${WORKSPACE}_dev", "app_T-1_dev"},
		{"${PORT0}", "5002"},
		{"${UNKNOWN} stays", "${UNKNOWN} stays"},
		{"no tokens", "no tokens"},
	}
	for _, tc := range cases {
		if got := wsp.Subst(tc.in, vars); got != tc.want {
			t.Errorf("Subst(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRuntimeVars(t *testing.T) {
	vars := wsp.RuntimeVars(testCfg(), "T-1", "app", 1)
	for k, want := range map[string]string{"WORKSPACE": "T-1", "PROJECT": "app", "PORT0": "5002", "PORT1": "5003"} {
		if vars[k] != want {
			t.Errorf("%s = %q, want %q", k, vars[k], want)
		}
	}
	if _, ok := wsp.RuntimeVars(testCfg(), "T-1", "", 1)["PROJECT"]; ok {
		t.Error("PROJECT must be absent when no project is given")
	}
}

func TestResolvedEnv(t *testing.T) {
	env := wsp.ResolvedEnv(testCfg(), "T-1", "app", 1)
	for k, want := range map[string]string{
		"RAILS_ENV": "development",
		"SHARED":    "project",                          // project overrides global
		"DB_NAME":   "app_T-1_dev",                      // substituted
		"URL":       "http://localhost:5002/${UNKNOWN}", // unknown token untouched
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
}
