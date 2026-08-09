package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"git.internal/cat/claude-workspaces-go/internal/gitx"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// newExecCmd builds `workspace exec <workspace> [project] <command> [args…]`:
// replace THIS process with the command, run inside the workspace under the
// curated environment. True replacement (unix.Exec, i.e. execve), not a child:
// the command's pid, signals, exit code and terminal are all first-class —
// `workspace exec T-1 app psql` behaves exactly like running psql there.
//
// The v1 env-poisoning bug is closed by construction: the ONLY environment the
// syscall is handed is wsp.CommandEnv's curated slice (allowlisted parent vars
// + sanitized PATH + the resolved workspace/project overlay). There is no
// other exec path and no inherited environment — a parent-only var cannot
// reach the command no matter what launched this process.
//
// The optional project is SNIFFED, not flagged: if the first argument after
// the workspace names a CONFIGURED project, it is the project (cwd = its
// worktree, which must be checked out; its env overlay applies). Anything else
// is the command (cwd = the workspace dir, global env only). The ambiguity
// this buys — a command that happens to be named like a project — is resolved
// by `--` (arguments after it are never project-sniffed) or by an explicit
// path (`./app`), the decided v1 behavior.
//
// argv[0] resolution deliberately does NOT use exec.LookPath: LookPath honors
// this PROCESS's env ($PATH, and on some platforms $PATHEXT), and the whole
// point is that the command runs under the curated env, not ours. lookPathIn
// searches the PATH extracted from the curated slice instead.
func newExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <workspace> [project] <command> [args...]",
		Short: "Replace this process with a command in the workspace's curated environment",
		// At least the workspace and a command; fewer is a usage error →
		// exit 2 (spec §9). (A sniffed project can still leave the command
		// slot empty — that is re-checked below, after the sniff.)
		Args: usageArgs(cobra.MinimumNArgs(2)),
		// --json is inherited and deliberately meaningless here: after the
		// replacement the output belongs to the command, not to this tool.
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, reg, err := loadRoot()
			if err != nil {
				return err
			}
			ws, err := wsp.Resolve(reg, args[0]) // ErrNotFound → exit 3
			if err != nil {
				return err
			}

			args, dash := stripDash(args, cmd.ArgsLenAtDash())
			project := ""
			rest := args[1:]
			// The sniff: args[1] is the project iff it names a configured
			// project AND sits strictly before any `--` (dash > 1 keeps
			// `exec T-1 -- app` meaning "the command app").
			if len(rest) > 0 && (dash == -1 || dash > 1) && cfg.Projects[rest[0]] != nil {
				project = rest[0]
				rest = rest[1:]
			}
			if len(rest) == 0 {
				if project != "" {
					return xerr.Wrap(xerr.ErrUsage, fmt.Errorf(
						"%q names a configured project, so a command must follow it; to run a command NAMED %q, put it after `--` or give a path",
						project, project))
				}
				return xerr.Wrap(xerr.ErrUsage, fmt.Errorf("a command is required"))
			}

			dir := ws.Dir
			if project != "" {
				dir = wsp.ProjectDir(ws, cfg, project)
				// Plain error, exit 1: the identifier resolved fine (the
				// project IS configured), this workspace just does not
				// contain it — a runtime condition, not a lookup failure.
				if !gitx.IsWorkTreeRoot(dir) {
					return fmt.Errorf("project %q is not checked out in %s (run: workspace checkout %s %s)",
						project, ws.Name(), ws.Name(), project)
				}
			}

			env := wsp.CommandEnv(cfg, project, ws.Alloc.TaskID, ws.Alloc.Index)
			path, err := lookPathIn(pathFrom(env), rest[0])
			if err != nil {
				return err // plain exit 1: the decided row's "natural exec error"
			}
			// The syscall takes no dir, so the process chdirs itself — last,
			// after every failure that should leave this process usable has
			// had its chance (the in-process unit tests rely on that order).
			if err := os.Chdir(dir); err != nil {
				return err
			}
			// unix.Exec never returns on success: from here the command owns
			// the process and testscript/exit codes see IT, not us. Reaching
			// the return means execve itself failed (ENOENT on an explicit
			// path, EACCES, a bad interpreter …) — a plain exit-1 error.
			return fmt.Errorf("exec %s: %w", rest[0], unix.Exec(path, rest, env))
		},
	}
	// Flags stop at the first positional: everything after the workspace name
	// belongs to the command being exec'd (`exec T-1 ls -la` must not parse
	// -la), same reason plain `kubectl exec`/`ssh` stop early.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

// stripDash normalizes the two ways a `--` separator reaches us. When pflag
// consumes it (it appeared before any positional), dash is already its index
// and args are clean. But with interspersed parsing off, a `--` AFTER the
// first positional — the common spelling, `exec T-1 -- app` — is passed
// through as a literal argument and ArgsLenAtDash stays -1; the first such
// literal is removed here and its index reported instead. Later occurrences
// are the command's own arguments and pass through untouched.
func stripDash(args []string, dash int) ([]string, int) {
	if dash >= 0 {
		return args, dash
	}
	for i, a := range args {
		if a == "--" {
			return append(append([]string{}, args[:i]...), args[i+1:]...), i
		}
	}
	return args, -1
}

// pathFrom extracts the PATH value from a curated "K=V" env slice; "" when the
// slice has none (a parent with no PATH at all — lookPathIn then finds nothing,
// and only explicit paths can run).
func pathFrom(env []string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			return v
		}
	}
	return ""
}

// lookPathIn resolves command against the GIVEN path variable — the curated
// env's PATH, not this process's, which is the property exec.LookPath cannot
// provide (it reads the process env). A command containing a path separator
// bypasses the search and is returned verbatim: it names a file, and the exec
// syscall reports the honest error if the file is not runnable (relative
// paths resolve against the target dir, since the chdir happens before the
// exec). Search hits require a regular file with any execute bit.
//
// Empty PATH segments (POSIX's "current directory" spelling) are deliberately
// NOT searched: this process's cwd and the command's eventual cwd differ, so a
// hit here could name a different file than the one exec'd. cwd-relative
// execution is the explicit-path spelling (`./cmd`).
func lookPathIn(pathVar, command string) (string, error) {
	if strings.Contains(command, "/") {
		return command, nil
	}
	for _, dir := range strings.Split(pathVar, ":") {
		if dir == "" {
			continue
		}
		cand := filepath.Join(dir, command)
		if info, err := os.Stat(cand); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	return "", fmt.Errorf("command %q not found in the workspace's curated PATH", command)
}
