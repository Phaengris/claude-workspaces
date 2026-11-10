package config

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Phaengris/chaban/v2/internal/xerr"
)

// specCanResume is the spec's can_resume line, verbatim.
const specCanResume = `find ~/.claude/projects/$(pwd | sed 's/[^A-Za-z0-9]/-/g') ~/.claude/projects/$(pwd -P | sed 's/[^A-Za-z0-9]/-/g') -maxdepth 1 -name '*.jsonl' 2>/dev/null | grep -q .`

func TestClaudeAgentBlock(t *testing.T) {
	a := ClaudeAgent()
	if a.Start != "claude" || a.Resume != "claude --continue" || a.InstructionsFile != "CLAUDE.md" {
		t.Errorf("unexpected built-in block: %+v", a)
	}
	if a.CanResume != specCanResume {
		t.Errorf("CanResume = %q, want the spec's line", a.CanResume)
	}
	a.Start, a.CanResume = "mutated", "mutated"
	if b := ClaudeAgent(); b.Start != "claude" || b.CanResume != specCanResume {
		t.Errorf("mutating a returned block leaked into the next call: %+v", b)
	}
	y := ClaudeAgentYAML()
	y[0] = 'X'
	if ClaudeAgentYAML()[0] == 'X' {
		t.Error("ClaudeAgentYAML must return a copy")
	}
}

func TestInstructionsFile(t *testing.T) {
	for name, tc := range map[string]struct{ yml, want string }{
		"no block":       {"projects: {}", "CLAUDE.md"},
		"null block":     {"agent:\nprojects: {}", "CLAUDE.md"},
		"no field":       {"agent: {start: aider}", "AGENTS.md"},
		"empty field":    {`agent: {start: aider, instructions_file: ""}`, "AGENTS.md"},
		"explicit field": {"agent: {start: gemini, instructions_file: GEMINI.md}", "GEMINI.md"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tc.yml))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.InstructionsFile(); got != tc.want {
				t.Errorf("InstructionsFile() = %q, want %q", got, tc.want)
			}
		})
	}
}

// encodeProjectDir is an independent encoding of a directory the way Claude
// names its project dir: every byte outside [A-Za-z0-9] becomes '-'.
func encodeProjectDir(p string) string {
	b := []byte(p)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '-'
		}
	}
	return string(b)
}

// TestClaudeCanResumeLine runs the shipped can_resume line in real shells,
// with a redirected HOME, for a root with a space and a symlinked route.
func TestClaudeCanResumeLine(t *testing.T) {
	line := ClaudeAgent().CanResume
	for _, shell := range []string{"sh", "bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			path, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s not installed", shell)
			}
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(base, "home")
			root := filepath.Join(base, "my root")
			ws := filepath.Join(root, "T-1_a_b")
			none := filepath.Join(root, "T-2_none")
			for _, d := range []string{home, ws, none} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			hist := filepath.Join(home, ".claude", "projects", encodeProjectDir(ws))
			if err := os.MkdirAll(hist, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(hist, "x.jsonl"), []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(base, "link")
			if err := os.Symlink(root, link); err != nil {
				t.Fatal(err)
			}

			run := func(dir, pwd string) int {
				cmd := exec.Command(path, "-lc", line)
				cmd.Dir = dir
				cmd.Env = []string{"HOME=" + home, "PWD=" + pwd, "PATH=" + os.Getenv("PATH"), "XDG_CONFIG_HOME=" + filepath.Join(home, ".config")}
				err := cmd.Run()
				var ee *exec.ExitError
				switch {
				case err == nil:
					return 0
				case errors.As(err, &ee):
					return ee.ExitCode()
				}
				t.Fatalf("run: %v", err)
				return -1
			}
			if c := run(ws, ws); c != 0 {
				t.Errorf("real path: exit %d, want 0", c)
			}
			if c := run(filepath.Join(link, "T-1_a_b"), filepath.Join(link, "T-1_a_b")); c != 0 {
				t.Errorf("symlinked route: exit %d, want 0", c)
			}
			if c := run(none, none); c != 1 {
				t.Errorf("sibling without history: exit %d, want 1", c)
			}
		})
	}
}

func TestAgentValidation(t *testing.T) {
	for name, tc := range map[string]struct{ yml, want string }{
		"empty block":          {"agent: {}", "agent: start is required"},
		"resume only":          {"agent: {start: a, resume: b}", "agent: resume and can_resume go together"},
		"can_resume only":      {"agent: {start: a, can_resume: 'true'}", "agent: resume and can_resume go together"},
		"instructions subdir":  {"agent: {start: a, instructions_file: sub/AGENTS.md}", "agent: instructions_file must be a plain file name"},
		"instructions dot":     {"agent: {start: a, instructions_file: .}", "agent: instructions_file must be a plain file name"},
		"instructions dotdot":  {"agent: {start: a, instructions_file: ..}", "agent: instructions_file must be a plain file name"},
		"instructions absolut": {"agent: {start: a, instructions_file: /AGENTS.md}", "agent: instructions_file must be a plain file name"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.yml))
			if !errors.Is(err, xerr.ErrConfig) {
				t.Fatalf("want xerr.ErrConfig, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should contain %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAgentNullAndFull(t *testing.T) {
	cfg, err := Load(writeConfig(t, "agent:\nprojects: {}"))
	if err != nil || cfg.Agent != nil {
		t.Fatalf("null agent: want loads with nil Agent, got %v, %+v", err, cfg)
	}
	cfg, err = Load(writeConfig(t, `agent:
  start: aider --yes
  resume: aider --restore
  can_resume: test -f .aider.chat.history.md
  instructions_file: CONVENTIONS.md
`))
	if err != nil {
		t.Fatal(err)
	}
	want := Agent{Start: "aider --yes", Resume: "aider --restore", CanResume: "test -f .aider.chat.history.md", InstructionsFile: "CONVENTIONS.md"}
	if cfg.Agent == nil || *cfg.Agent != want {
		t.Errorf("Agent = %+v, want %+v", cfg.Agent, want)
	}
}
