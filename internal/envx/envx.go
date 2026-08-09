// Package envx builds the curated environment for every spawned process
// (spec §6). The design is a documented compromise: an allowlist of safe
// vars + a sanitized PATH that keeps version-manager shims (per-directory
// dispatchers) but strips concrete per-version bins, so each worktree's own
// .ruby-version / .tool-versions resolves by cwd. Fail-safe: over-keeping a
// segment only reproduces pin-to-launch-version behavior, never worse.
package envx

import (
	"os"
	"slices"
	"strings"
)

// allowlist bounds the safe vars; anything not named here is dropped, so a
// new version manager's vars can never leak no matter what they're called.
// The user extends it per config with env_allow — never by editing this list.
var allowlist = []string{
	"HOME", "USER", "LOGNAME", "SHELL", "TERM", "TERM_PROGRAM",
	"LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "LC_COLLATE", "LC_NUMERIC", "LC_TIME",
	"TZ",
	"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY",
	"SSH_AUTH_SOCK", "SSH_AGENT_PID",
	"GPG_AGENT_INFO", "GNUPGHOME",
	"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
	"DBUS_SESSION_BUS_ADDRESS",
}

// versionManagerPrefixes covers the version-*selection* vector only: pin vars
// exported by shims into our process. Gem/module-path vars (RUBYOPT, GEM_HOME,
// PYTHONPATH, …) are simply not allowlisted — once selection is correct the
// shim re-derives them per command. New manager → add one prefix.
var versionManagerPrefixes = []string{
	"RBENV_", "PYENV_", "NODENV_", "PLENV_", "GOENV_", "RUBYENV_", "ASDF_", "MISE_", "__MISE_",
}

// SanitizePATH strips concrete per-version install bins — segments containing
// /versions/ or /installs/ and ending in /bin (with or without one trailing
// slash) — leaving shims reachable. Other segments, including empty ones,
// pass through in order. Idempotent.
func SanitizePATH(path string) string {
	if path == "" {
		return ""
	}
	segs := strings.Split(path, ":")
	kept := segs[:0]
	for _, seg := range segs {
		if !concreteVersionBin(seg) {
			kept = append(kept, seg)
		}
	}
	return strings.Join(kept, ":")
}

func concreteVersionBin(seg string) bool {
	if seg == "" {
		return false
	}
	trimmed := strings.TrimSuffix(seg, "/")
	return (strings.Contains(trimmed, "/versions/") || strings.Contains(trimmed, "/installs/")) &&
		strings.HasSuffix(trimmed, "/bin")
}

// Curated builds the complete child environment from parent entries in
// os.Environ() "K=V" form: allowlisted parent vars plus extraAllow (config
// env_allow), sanitized PATH, then overlay (workspace/project env) merged
// last so it always wins. Version-manager pin vars are dropped by prefix
// unless named exactly in extraAllow — an explicit user allowance outranks
// the prefix drop (spec §6 escape hatch). PATH is always sanitized, even if
// named in extraAllow. Entries without "=" or with an empty key are dropped.
// Output is sorted for determinism and is assigned to exec.Cmd.Env, which is
// total — nothing else is inherited.
func Curated(parent []string, extraAllow []string, overlay map[string]string) []string {
	baseAllowed := make(map[string]bool, len(allowlist))
	for _, k := range allowlist {
		baseAllowed[k] = true
	}
	extraAllowed := make(map[string]bool, len(extraAllow))
	for _, k := range extraAllow {
		extraAllowed[k] = true
	}

	env := make(map[string]string)
	for _, kv := range parent {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			continue
		}
		switch {
		case k == "PATH":
			env["PATH"] = SanitizePATH(v)
		case extraAllowed[k]:
			env[k] = v
		case baseAllowed[k] && !hasVersionManagerPrefix(k):
			env[k] = v
		}
	}
	for k, v := range overlay {
		env[k] = v
	}

	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}

func hasVersionManagerPrefix(k string) bool {
	for _, p := range versionManagerPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// SanitizeSelf undoes, in place, the version-manager activation that launched
// this process (PATH prepends + pin vars), so every later inherit-spawn
// (claude, exec) is clean by default. Called once at startup. Idempotent.
func SanitizeSelf() {
	if path, ok := os.LookupEnv("PATH"); ok {
		os.Setenv("PATH", SanitizePATH(path))
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if hasVersionManagerPrefix(k) {
			os.Unsetenv(k)
		}
	}
}
