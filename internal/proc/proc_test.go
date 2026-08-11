package proc_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Phaengris/claude-workspaces/internal/envx"
	"github.com/Phaengris/claude-workspaces/internal/proc"
)

// All Run tests pin SHELL to /bin/sh: the developer's login shell may be fish,
// whose syntax differs, and the contract resolves SHELL from the CURRENT
// process env, so t.Setenv is exactly the knob. Commands assert via file
// contents, never stdout — `-lc` runs login init, which may write to stdout.
//
// The composed-environment tests (allow merging, resolved overlay) live with
// wsp.CommandEnv; here the env slice is built with envx.Curated directly, so
// proc's tests exercise only proc's own contract.

func TestRunSuccess(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	env := envx.Curated(os.Environ(), nil, map[string]string{"DB_NAME": "app_T42"})

	if err := proc.Run(dir, `printf %s "$DB_NAME" > out`, env); err != nil {
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

	env := envx.Curated(os.Environ(), []string{"ALLOWED_Y"}, nil)
	dir := t.TempDir()

	script := `test -z "$SECRET_X" || printf %s leaked > secret
printf %s "$HOME" > home
printf %s "$ALLOWED_Y" > allowed`
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

func TestRunNilEnvIsEmpty(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("SECRET_X", "leak-me-not")
	dir := t.TempDir()

	// nil env must mean EMPTY, not exec.Cmd's documented "inherit the parent
	// process env" — inheriting is the exact failure mode proc exists to
	// prevent. The probe may fail (no PATH), so ignore Run's error and let
	// the file assertion decide.
	_ = proc.Run(dir, `test -z "$SECRET_X" || printf %s leaked > secret`, nil)

	if _, err := os.Stat(filepath.Join(dir, "secret")); err == nil {
		t.Error("SECRET_X leaked into child spawned with nil env: nil must mean empty, not parent env")
	}
}

func TestRunFailureStdoutFallback(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	env := envx.Curated(os.Environ(), nil, map[string]string{"HOME": dir})

	err := proc.Run(dir, `echo oops; exit 1`, env)
	if err == nil {
		t.Fatal("Run returned nil for a command exiting 1")
	}
	if !strings.Contains(err.Error(), "oops") {
		t.Errorf("error %q does not fall back to the first stdout line %q", err, "oops")
	}
}

func TestRunFailureExitStatusFallback(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	env := envx.Curated(os.Environ(), nil, map[string]string{"HOME": dir})

	err := proc.Run(dir, `exit 3`, env)
	if err == nil {
		t.Fatal("Run returned nil for a command exiting 3")
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("error %q does not fall back to the exit status text", err)
	}
}
