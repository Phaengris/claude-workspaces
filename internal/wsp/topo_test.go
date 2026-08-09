package wsp_test

import (
	"slices"
	"strings"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

func cfgWithDeps(deps map[string][]string) *config.Config {
	cfg := &config.Config{Projects: map[string]*config.Project{}}
	for name, d := range deps {
		cfg.Projects[name] = &config.Project{Repo: "/r", Depends: config.StringList(d)}
	}
	return cfg
}

func TestTopoOrder(t *testing.T) {
	cases := []struct {
		name  string
		deps  map[string][]string
		names []string
		want  []string
	}{
		{
			// "a depends on b" means b must be set up first.
			name:  "single edge puts the dependency first",
			deps:  map[string][]string{"a": {"b"}, "b": nil},
			names: []string{"a", "b"},
			want:  []string{"b", "a"},
		},
		{
			name:  "chain",
			deps:  map[string][]string{"c": {"b"}, "b": {"a"}, "a": nil},
			names: []string{"c", "b", "a"},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "independent projects come out sorted",
			deps:  map[string][]string{"c": nil, "a": nil, "b": nil},
			names: []string{"c", "a", "b"},
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "ties among ready projects are sorted",
			deps:  map[string][]string{"db": nil, "web": {"db"}, "api": {"db"}},
			names: []string{"web", "api", "db"},
			want:  []string{"db", "api", "web"},
		},
		{
			name:  "edge to a project outside the set is ignored",
			deps:  map[string][]string{"web": {"db"}, "db": nil},
			names: []string{"web"},
			want:  []string{"web"},
		},
		{
			name:  "unconfigured name has no dependencies",
			deps:  map[string][]string{"a": nil},
			names: []string{"stray", "a"},
			want:  []string{"a", "stray"},
		},
		{
			name:  "empty set",
			deps:  map[string][]string{"a": nil},
			names: nil,
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := wsp.TopoOrder(cfgWithDeps(tc.deps), tc.names)
			if err != nil {
				t.Fatalf("TopoOrder: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("TopoOrder(%v) = %v, want %v", tc.names, got, tc.want)
			}
		})
	}
}

func TestTopoOrderCycle(t *testing.T) {
	cfg := cfgWithDeps(map[string][]string{"a": {"b"}, "b": {"a"}})
	_, err := wsp.TopoOrder(cfg, []string{"a", "b"})
	if err == nil {
		t.Fatal("a cycle must be reported, not silently truncated")
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "b") {
		t.Errorf("cycle error should name the involved projects, got %q", err)
	}
}

// TestTopoOrderDeterministic guards against map iteration leaking into the
// result: the same input must give the same order every time, not merely a
// valid one.
func TestTopoOrderDeterministic(t *testing.T) {
	cfg := cfgWithDeps(map[string][]string{
		"db": nil, "cache": nil, "api": {"db", "cache"}, "web": {"api"}, "docs": nil,
	})
	names := []string{"web", "docs", "api", "cache", "db"}
	// The lexicographically smallest valid order: at every step the ready
	// project that sorts first goes next, so "api" (freed by db) precedes the
	// always-ready "docs".
	want := []string{"cache", "db", "api", "docs", "web"}
	for i := 0; i < 20; i++ {
		got, err := wsp.TopoOrder(cfg, names)
		if err != nil {
			t.Fatalf("TopoOrder: %v", err)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("run %d: TopoOrder = %v, want %v", i, got, want)
		}
	}
}
