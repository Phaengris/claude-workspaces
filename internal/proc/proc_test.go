package proc_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/envx"
	"git.internal/cat/claude-workspaces-go/internal/proc"
)

// All Run tests pin SHELL to /bin/sh: the developer's login shell may be fish,
// whose syntax differs, and the contract resolves SHELL from the CURRENT
// process env, so t.Setenv is exactly the knob. Commands assert via file
// contents, never stdout — `-lc` runs login init, which may write to stdout.

// envValue extracts K from a "K=V" slice; second return reports presence.
func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

func TestRunSuccess(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	env := envx.Curated(os.Environ(), nil, map[string]string{"DB_NAME": "app_T42"})

	if err := proc.Run(dir, `echo -n "$DB_NAME" > out`, env); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatalf("command did not create out in dir: %v", err)
	}
	if string(got) != "app_T42" {
		t.Errorf("out = %q, want %q (overlay var not visible to command)", got, "app_T42")
	}
}

func TestRunFailureFirstStderrLine(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	// HOME pointed at the empty temp dir keeps user login init silent, so
	// the first stderr line is deterministically ours.
	env := envx.Curated(os.Environ(), nil, map[string]string{"HOME": dir})

	err := proc.Run(dir, `echo "first bad thing" >&2; echo "second detail" >&2; exit 1`, env)
	if err == nil {
		t.Fatal("Run returned nil for a command exiting 1")
	}
	if !strings.Contains(err.Error(), "first bad thing") {
		t.Errorf("error %q does not contain first stderr line %q", err, "first bad thing")
	}
	if strings.Contains(err.Error(), "second detail") {
		t.Errorf("error %q contains the second stderr line; want only the first", err)
	}
}

func TestRunEnvIsTotal(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("SECRET_X", "leak-me-not")
	t.Setenv("ALLOWED_Y", "visible")

	cfg := &config.Config{EnvAllow: []string{"ALLOWED_Y"}}
	env := proc.CommandEnv(cfg, "", "T1", 0)
	dir := t.TempDir()

	script := `test -z "$SECRET_X" || echo -n leaked > secret
echo -n "$HOME" > home
echo -n "$ALLOWED_Y" > allowed`
	if err := proc.Run(dir, script, env); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "secret")); err == nil {
		t.Error("SECRET_X leaked into the child: env must be total, not inherited")
	}
	home, err := os.ReadFile(filepath.Join(dir, "home"))
	if err != nil {
		t.Fatalf("reading home probe: %v", err)
	}
	if want := os.Getenv("HOME"); string(home) != want {
		t.Errorf("child HOME = %q, want allowlisted parent value %q", home, want)
	}
	allowed, err := os.ReadFile(filepath.Join(dir, "allowed"))
	if err != nil {
		t.Fatalf("reading allowed probe: %v", err)
	}
	if string(allowed) != "visible" {
		t.Errorf("child ALLOWED_Y = %q, want %q (env_allow not honored)", allowed, "visible")
	}
}

func TestCommandEnvMergesEnvAllow(t *testing.T) {
	t.Setenv("GLOBAL_VAR", "g")
	t.Setenv("PROJ_VAR", "p")

	cfg := &config.Config{
		EnvAllow: []string{"GLOBAL_VAR"},
		Env:      map[string]string{"DB_NAME": "db_${WORKSPACE}"},
		Projects: map[string]*config.Project{
			"api": {EnvAllow: []string{"PROJ_VAR"}},
		},
	}

	env := proc.CommandEnv(cfg, "api", "T7", 0)
	if v, ok := envValue(env, "GLOBAL_VAR"); !ok || v != "g" {
		t.Errorf("GLOBAL_VAR = %q, %v; want %q via global env_allow", v, ok, "g")
	}
	if v, ok := envValue(env, "PROJ_VAR"); !ok || v != "p" {
		t.Errorf("PROJ_VAR = %q, %v; want %q via project env_allow", v, ok, "p")
	}
	if v, ok := envValue(env, "DB_NAME"); !ok || v != "db_T7" {
		t.Errorf("DB_NAME = %q, %v; want %q from resolved overlay", v, ok, "db_T7")
	}

	env = proc.CommandEnv(cfg, "unknown", "T7", 0)
	if v, ok := envValue(env, "GLOBAL_VAR"); !ok || v != "g" {
		t.Errorf("unknown project: GLOBAL_VAR = %q, %v; want %q (global allow stands alone)", v, ok, "g")
	}
	if _, ok := envValue(env, "PROJ_VAR"); ok {
		t.Error("unknown project: PROJ_VAR present; project env_allow must not apply")
	}
}
