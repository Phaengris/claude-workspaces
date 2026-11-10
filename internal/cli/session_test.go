package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Phaengris/chaban/v2/internal/config"
	"github.com/Phaengris/chaban/v2/internal/xerr"
)

func TestParseSessionFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		noResume bool
		rest     []string
		errIs    error
		errText  string
	}{
		{"empty", []string{}, false, nil, nil, ""},
		{"short", []string{"-R"}, true, nil, nil, ""},
		{"long", []string{"--no-resume"}, true, nil, nil, ""},
		{"passthrough", []string{"--model", "opus"}, false, []string{"--model", "opus"}, nil, ""},
		{"after dashdash verbatim", []string{"-R", "--", "-S", "--claude-no-resume", "-R"}, true, []string{"-S", "--claude-no-resume", "-R"}, nil, ""},
		{"S", []string{"-S"}, false, nil, xerr.ErrUsage, "skip-permissions is no longer injected; it is whatever your agent: commands say"},
		{"long S", []string{"--claude-no-skip-permissions"}, false, nil, xerr.ErrUsage, "skip-permissions is no longer injected; it is whatever your agent: commands say"},
		{"old resume", []string{"--claude-no-resume"}, false, nil, xerr.ErrUsage, "use -R/--no-resume"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			nr, rest, err := parseSessionFlags(tc.args)
			if tc.errIs != nil {
				if !errors.Is(err, tc.errIs) || !strings.Contains(err.Error(), tc.errText) {
					t.Fatalf("err = %v, want %v containing %q", err, tc.errIs, tc.errText)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if nr != tc.noResume || !reflect.DeepEqual(rest, tc.rest) {
				t.Fatalf("got %v %#v, want %v %#v", nr, rest, tc.noResume, tc.rest)
			}
		})
	}
}

func TestQuoteArg(t *testing.T) {
	for in, want := range map[string]string{
		"":        `''`,
		"abc":     `'abc'`,
		"it's":    `'it'\''s'`,
		`a\b`:     `'a'\\'b'`,
		"$HOME *": `'$HOME *'`,
	} {
		if got := quoteArg(in); got != want {
			t.Errorf("quoteArg(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestQuoteArgSurvivesShells(t *testing.T) {
	inputs := []string{"it's", `back\slash`, `trail\`, "$HOME *", "two\nlines", `""`, `\'mix'\`}
	var quoted []string
	for _, in := range inputs {
		quoted = append(quoted, quoteArg(in))
	}
	script := "printf '%s\\0' " + strings.Join(quoted, " ")
	for _, sh := range []string{"sh", "bash", "zsh", "fish"} {
		t.Run(sh, func(t *testing.T) {
			path, err := exec.LookPath(sh)
			if err != nil {
				t.Skipf("%s not installed", sh)
			}
			out, err := exec.Command(path, "-c", script).Output()
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			if !reflect.DeepEqual(got, inputs) {
				t.Fatalf("got %q, want %q", got, inputs)
			}
		})
	}
}

func shimDir(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if name != "" {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestResolveAgent(t *testing.T) {
	t.Run("configured", func(t *testing.T) {
		a := &config.Agent{Start: "x"}
		var stderr strings.Builder
		got, err := resolveAgent(&config.Config{Agent: a}, &stderr)
		if err != nil || got.Start != "x" || stderr.Len() != 0 {
			t.Fatalf("got %+v %v stderr=%q", got, err, stderr.String())
		}
	})
	t.Run("fallback", func(t *testing.T) {
		t.Setenv("PATH", shimDir(t, "claude"))
		var stderr strings.Builder
		got, err := resolveAgent(&config.Config{}, &stderr)
		if err != nil || !reflect.DeepEqual(got, config.ClaudeAgent()) {
			t.Fatalf("got %+v %v", got, err)
		}
		want := "chaban: no agent: in config.yml, starting claude (found on PATH); add an agent: block to choose\n"
		if stderr.String() != want {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})
	t.Run("no claude", func(t *testing.T) {
		t.Setenv("PATH", shimDir(t, ""))
		var stderr strings.Builder
		_, err := resolveAgent(&config.Config{}, &stderr)
		if !errors.Is(err, xerr.ErrConfig) || !strings.Contains(err.Error(), string(config.ClaudeAgentYAML())) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestProbeCanResume(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	probe := func(cmd string, d time.Duration) (bool, error) {
		return probeCanResume(&config.Agent{CanResume: cmd}, t.TempDir(), os.Environ(), d)
	}
	if ok, err := probe("exit 0", 5*time.Second); !ok || err != nil {
		t.Fatalf("exit 0: %v %v", ok, err)
	}
	if ok, err := probe("exit 1", 5*time.Second); ok || err != nil {
		t.Fatalf("exit 1: %v %v", ok, err)
	}
	_, err := probe("echo boom >&2; exit 3", 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "exit 3") || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "-R") {
		t.Fatalf("exit 3: %v", err)
	}
	start := time.Now()
	_, err = probe("sleep 30 & sleep 30", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout: %v", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("timeout took %v", el)
	}
	_, err = probe("echo SECRETOUT; exit 3", 5*time.Second)
	if err == nil || strings.Contains(err.Error(), "SECRETOUT") {
		t.Fatalf("stdout leaked: %v", err)
	}
}

func TestAgentCommandLine(t *testing.T) {
	a := &config.Agent{Start: "go", Resume: "go -r"}
	for _, tc := range []struct {
		resume bool
		args   []string
		want   string
	}{
		{false, nil, "go"},
		{true, nil, "go -r"},
		{false, []string{"a b", "it's"}, `go 'a b' 'it'\''s'`},
		{true, []string{"x"}, "go -r 'x'"},
	} {
		if got := agentCommandLine(a, tc.resume, tc.args); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
