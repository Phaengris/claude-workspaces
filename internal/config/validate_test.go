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

func TestValidateOK(t *testing.T) {
	cfg := &Config{Projects: map[string]*Project{
		"a": {Repo: "/r", Depends: StringList{"b"}},
		"b": {Repo: "/r"},
	}}
	if err := cfg.validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}
