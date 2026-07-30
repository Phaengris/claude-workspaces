package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

func writeConfig(t *testing.T, yml string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadExpandsTildeInRepo(t *testing.T) {
	root := writeConfig(t, "projects: {p: {repo: ~/dev/p}}")
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, "dev/p"); cfg.Projects["p"].Repo != want {
		t.Errorf("Repo = %q, want %q", cfg.Projects["p"].Repo, want)
	}
}

func TestLoadWrapsErrorsAsConfig(t *testing.T) {
	for name, yml := range map[string]string{
		"bad yaml":       "projects: [::",
		"unknown key":    "projects: {p: {repo: /r, comand: x}}",
		"bad template":   "projects: {p: {template: nope}}",
		"invalid config": "projects: {p: {}}",
	} {
		t.Run(name, func(t *testing.T) {
			root := writeConfig(t, yml)
			_, err := Load(root)
			if !errors.Is(err, xerr.ErrConfig) {
				t.Errorf("Load error must wrap xerr.ErrConfig, got %v", err)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(t.TempDir())
	if !errors.Is(err, xerr.ErrConfig) || !strings.Contains(err.Error(), "config.yml") {
		t.Errorf("missing config.yml must be an ErrConfig naming the file, got %v", err)
	}
}

func TestRootDirEnvOverride(t *testing.T) {
	t.Setenv("CLAUDE_WORKSPACES_ROOT_DIR", "/custom/root")
	got, err := RootDir()
	if err != nil || got != "/custom/root" {
		t.Errorf("RootDir() = %q, %v; want /custom/root", got, err)
	}
}

func TestRootDirDefault(t *testing.T) {
	t.Setenv("CLAUDE_WORKSPACES_ROOT_DIR", "")
	home, _ := os.UserHomeDir()
	got, err := RootDir()
	if err != nil || got != filepath.Join(home, "claude-workspaces") {
		t.Errorf("RootDir() = %q, %v; want %q", got, err, filepath.Join(home, "claude-workspaces"))
	}
}
