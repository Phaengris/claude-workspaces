// Package proc spawns foreground user commands (setup/teardown) under the
// tool's spawn contract (spec §6): `$SHELL -lc <command>` in the project dir
// with a CURATED environment assigned to exec.Cmd.Env — total, never the raw
// parent env. CommandEnv is the ONE place that curated slice is composed.
package proc

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"git.internal/cat/claude-workspaces-go/internal/config"
	"git.internal/cat/claude-workspaces-go/internal/envx"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

// Run executes command via `$SHELL -lc <command>` with cmd.Dir = dir and
// cmd.Env = env. SHELL is resolved from the CURRENT process env (fallback
// /bin/sh); if SHELL names a missing binary, exec fails naturally and the
// error carries the OS message — no second-guessing the user's setting.
// env is the complete child environment; the caller builds it (CommandEnv)
// and nothing else is inherited. A nil env means EMPTY, guarded explicitly:
// exec.Cmd's documented nil-Env semantics ("inherit the parent process env")
// are the exact failure mode this package exists to prevent.
// stdout and stderr are captured separately;
// on non-zero exit the error contains the first non-empty stderr line
// (fallback: first non-empty stdout line, fallback: the exit status).
// No timeout, no retry — foreground commands run to completion.
func Run(dir, command string, env []string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	if env == nil {
		env = []string{} // nil would make exec.Cmd inherit the raw parent env
	}
	cmd := exec.Command(shell, "-lc", command)
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return nil
	}
	reason := firstNonEmptyLine(stderr.String())
	if reason == "" {
		reason = firstNonEmptyLine(stdout.String())
	}
	if reason == "" {
		reason = err.Error() // exit status, or the exec failure itself
	}
	return fmt.Errorf("command failed: %s", reason)
}

// firstNonEmptyLine returns the first line of s that is non-empty after
// trimming whitespace (including the CR of CRLF endings); "" if none.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// CommandEnv composes the complete curated environment for one spawned
// command: envx.Curated over os.Environ() with the allowlist extended by
// cfg.EnvAllow ∪ the named project's EnvAllow, and wsp.ResolvedEnv as the
// winning overlay. An empty or unknown project contributes no extra
// allowances — the global env_allow stands alone. This wires config's
// deferred env_allow merge and is the only composer of spawn environments.
func CommandEnv(cfg *config.Config, project, taskID string, index int) []string {
	allow := append([]string(nil), cfg.EnvAllow...)
	if p := cfg.Projects[project]; p != nil {
		allow = append(allow, p.EnvAllow...)
	}
	overlay := wsp.ResolvedEnv(cfg, taskID, project, index)
	return envx.Curated(os.Environ(), allow, overlay)
}
