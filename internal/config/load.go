package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/Phaengris/chaban/v2/internal/xerr"
)

// RootDir resolves the workspaces root: $CHABAN_ROOT_DIR overrides ~/workspaces.
// Every root is an independent universe with its own config and registry
// (spec §3). It errors when the home directory cannot be determined and no
// override is set, and on two upgrade guards (both ErrConfig, exit 4):
//
//   - CHABAN_ROOT_DIR unset but the old CLAUDE_WORKSPACES_ROOT_DIR set: the old
//     name is no longer read, so silently ignoring it would point the tool at
//     a different root than the user thinks.
//   - No override, ~/workspaces missing, ~/claude-workspaces present: install
//     creates a missing root and writes a config stub into it, so without this
//     guard an upgraded user would silently get a fresh, empty root.
//
// A Stat error other than not-exist on ~/workspaces counts as "exists": doubt
// reads as keep, so no guard fires and no new root is suggested.
func RootDir() (string, error) {
	if dir := os.Getenv("CHABAN_ROOT_DIR"); dir != "" {
		return dir, nil
	}
	if os.Getenv("CLAUDE_WORKSPACES_ROOT_DIR") != "" {
		return "", xerr.Wrap(xerr.ErrConfig, errors.New("CLAUDE_WORKSPACES_ROOT_DIR is no longer read: rename it to CHABAN_ROOT_DIR"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	root := filepath.Join(home, "workspaces")
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		legacy := filepath.Join(home, "claude-workspaces")
		if _, lerr := os.Stat(legacy); lerr == nil {
			return "", xerr.Wrap(xerr.ErrConfig, fmt.Errorf("%s exists, but the default root is now ~/workspaces: set CHABAN_ROOT_DIR=%s to keep using it, or move it", legacy, legacy))
		}
	}
	return root, nil
}

// Load reads <root>/config.yml: raw unmarshal → template expansion → strict
// typed decode → tilde expansion → validation. Any failure is wrapped
// xerr.ErrConfig (exit code 4). Strict decode runs AFTER expansion because
// template/params keys are consumed by expansion and are not part of the typed
// schema. A missing or empty file yields distinct outcomes: a missing file is
// an ErrConfig naming the path, an empty file is a valid empty *Config.
func Load(root string) (*Config, error) {
	path := filepath.Join(root, "config.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, xerr.Wrap(xerr.ErrConfig, err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, xerr.Wrap(xerr.ErrConfig, err)
	}
	if raw == nil {
		return &Config{}, nil
	}

	// Strict-decode source selection (implementation finding, 2026-07-30):
	// goccy's positioned errors reference the bytes it decodes. When no
	// templates are in play we decode the ORIGINAL file bytes, so [line:col]
	// positions point at the user's config.yml. When templates ARE in play the
	// expand + re-marshal round-trip is unavoidable, and positions then refer to
	// the regenerated (key-sorted, re-laid-out) document — so we flag that in the
	// error message rather than mislead. AST-level expansion is the future fix.
	toDecode := data
	templated := usesTemplates(raw)
	if templated {
		if err := expandTemplates(raw); err != nil {
			return nil, xerr.Wrap(xerr.ErrConfig, err)
		}
		clean, err := yaml.Marshal(raw)
		if err != nil {
			return nil, xerr.Wrap(xerr.ErrConfig, err)
		}
		toDecode = clean
	}

	cfg, err := decodeStrict(toDecode)
	if err != nil {
		if templated {
			err = fmt.Errorf("%w\n(note: line/column positions above refer to the template-expanded config, not the original %s)", err, path)
		}
		return nil, xerr.Wrap(xerr.ErrConfig, err)
	}
	for _, p := range cfg.Projects {
		if p != nil {
			p.Repo = ExpandTilde(p.Repo)
		}
	}
	if err := cfg.validate(); err != nil {
		return nil, xerr.Wrap(xerr.ErrConfig, err)
	}
	return cfg, nil
}

// ExpandTilde replaces a leading ~ with the home directory. Go has no
// built-in for this (the shell normally does it; config values bypass the
// shell) — v1's most-forgotten porting pitfall, so it lives in one place.
// Only a bare "~" or a "~/"-prefixed path is expanded; "~user" and mid-path
// tildes are returned unchanged, as is any path when the home dir is unknown.
func ExpandTilde(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}
