package config

import (
	"errors"
	"fmt"
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

// lineOf returns the 1-based line number of the first line in yml that
// contains needle, so position assertions derive the expected line from the
// fixture rather than hardcoding a magic number.
func lineOf(t *testing.T, yml, needle string) int {
	t.Helper()
	for i, line := range strings.Split(yml, "\n") {
		if strings.Contains(line, needle) {
			return i + 1
		}
	}
	t.Fatalf("needle %q not found in fixture", needle)
	return 0
}

// TestLoadNoTemplatePositionsAreExact pins Finding 1's fix: a config with no
// templates skips the expand/re-marshal round-trip and strict-decodes the
// ORIGINAL bytes, so goccy's [line:col] positions point at the user's file.
func TestLoadNoTemplatePositionsAreExact(t *testing.T) {
	yml := `values:
  PORT:
    start: 5000
    per_workspace: 10
projects:
  app:
    repo: /tmp/x
    bogus_key: nope
`
	wantLine := lineOf(t, yml, "bogus_key")
	root := writeConfig(t, yml)
	_, err := Load(root)
	if err == nil {
		t.Fatal("unknown key must be a strict-decode error")
	}
	if !errors.Is(err, xerr.ErrConfig) {
		t.Errorf("error must wrap xerr.ErrConfig, got %v", err)
	}
	marker := fmt.Sprintf("[%d:", wantLine)
	if !strings.Contains(err.Error(), marker) {
		t.Errorf("no-template config error must cite the user's line %d (%q); got: %v", wantLine, marker, err)
	}
}

// TestLoadTemplatedPositionsAreFlagged pins Finding 1's honesty note: a
// templated config still goes through expand + re-marshal, so its positions
// refer to the expanded form — the error must say so, and still wrap ErrConfig.
func TestLoadTemplatedPositionsAreFlagged(t *testing.T) {
	yml := `templates:
  base:
    params: [NAME]
    repo: /r/${NAME}
projects:
  app:
    template: base
    params: { NAME: app }
    bogus_key: nope
`
	root := writeConfig(t, yml)
	_, err := Load(root)
	if err == nil {
		t.Fatal("unknown key in a templated project must be a strict-decode error")
	}
	if !errors.Is(err, xerr.ErrConfig) {
		t.Errorf("error must wrap xerr.ErrConfig, got %v", err)
	}
	if !strings.Contains(err.Error(), "template-expanded") {
		t.Errorf("templated config error must flag that positions refer to the template-expanded form; got: %v", err)
	}
}

// TestLoadEmptyTemplatesBlockKeepsExactPositions pins the pairing between
// usesTemplates (an empty/null `templates:` declares nothing, so no round-trip)
// and decodeStrict (which must therefore tolerate the key still being in the
// bytes): such a config loads, and a strict-decode error cites the user's own
// line without the template-expanded caveat.
func TestLoadEmptyTemplatesBlockKeepsExactPositions(t *testing.T) {
	for name, tmpl := range map[string]string{"empty": "templates: {}", "null": "templates: ~"} {
		t.Run(name, func(t *testing.T) {
			ok := tmpl + "\nprojects:\n  app:\n    repo: /tmp/x\n"
			cfg, err := Load(writeConfig(t, ok))
			if err != nil {
				t.Fatalf("a declared-but-unused templates block must load: %v", err)
			}
			if cfg.Projects["app"].Repo != "/tmp/x" {
				t.Errorf("Repo = %q, want /tmp/x", cfg.Projects["app"].Repo)
			}

			bad := tmpl + "\nprojects:\n  app:\n    repo: /tmp/x\n    bogus_key: nope\n"
			wantLine := lineOf(t, bad, "bogus_key")
			_, err = Load(writeConfig(t, bad))
			if err == nil {
				t.Fatal("unknown key must be a strict-decode error")
			}
			if marker := fmt.Sprintf("[%d:", wantLine); !strings.Contains(err.Error(), marker) {
				t.Errorf("error must cite the user's line %d (%q); got: %v", wantLine, marker, err)
			}
			if strings.Contains(err.Error(), "template-expanded") {
				t.Errorf("no expansion happened, so the caveat must be absent; got: %v", err)
			}
		})
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
