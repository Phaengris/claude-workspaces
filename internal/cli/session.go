package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/Phaengris/chaban/v2/internal/config"
	"github.com/Phaengris/chaban/v2/internal/proc"
	"github.com/Phaengris/chaban/v2/internal/wsp"
	"github.com/Phaengris/chaban/v2/internal/xerr"
)

// canResumeTimeout bounds the can_resume probe.
const canResumeTimeout = 5 * time.Second

// parseSessionFlags scans args up to the first "--" for chaban's own session
// flags. The first "--" is dropped; everything after it, and every flag not
// recognised before it, goes to the agent verbatim.
func parseSessionFlags(args []string) (noResume bool, rest []string, err error) {
	for i, a := range args {
		switch a {
		case "--":
			return noResume, append(rest, args[i+1:]...), nil
		case "-R", "--no-resume":
			noResume = true
		case "-S", "--claude-no-skip-permissions":
			return false, nil, xerr.Wrap(xerr.ErrUsage, fmt.Errorf("%s: skip-permissions is no longer injected; it is whatever your agent: commands say", a))
		case "--claude-no-resume":
			return false, nil, xerr.Wrap(xerr.ErrUsage, errors.New("--claude-no-resume was renamed: use -R/--no-resume"))
		default:
			rest = append(rest, a)
		}
	}
	return noResume, rest, nil
}

var quoteArgEscaper = strings.NewReplacer(`\`, `'\\'`, `'`, `'\''`)

// quoteArg single-quotes s so sh, bash, zsh and fish all read it back as one
// literal word. A quote becomes: close the quote, backslash-quote, reopen it.
// A backslash becomes: close the quote, two backslashes, reopen it.
func quoteArg(s string) string {
	return "'" + quoteArgEscaper.Replace(s) + "'"
}

// fallbackProgram is the program the no-agent: fallback needs: the first word
// of the built-in Claude block's start command. found reports whether
// exec.LookPath finds it on chaban's own PATH. resolveAgent and doctor both ask
// this, so the rule lives in one place.
func fallbackProgram() (prog string, found bool) {
	prog = strings.Fields(config.ClaudeAgent().Start)[0]
	_, err := exec.LookPath(prog)
	return prog, err == nil
}

// resolveAgent returns the configured agent, or the built-in Claude block when
// config has none and claude is on chaban's PATH (noting that on stderr).
func resolveAgent(cfg *config.Config, stderr io.Writer) (*config.Agent, error) {
	if cfg.Agent != nil {
		return cfg.Agent, nil
	}
	prog, found := fallbackProgram()
	if !found {
		return nil, xerr.Wrap(xerr.ErrConfig, fmt.Errorf(
			"no agent: block in config.yml and no %s on PATH; add one, for example:\n\n%s",
			prog, config.ClaudeAgentYAML()))
	}
	fmt.Fprintf(stderr, "chaban: no agent: in config.yml, starting %s (found on PATH); add an agent: block to choose\n", prog)
	return config.ClaudeAgent(), nil
}

// probeCanResume runs the agent's can_resume command: exit 0 means resume,
// exit 1 means start fresh, anything else is an error. The command runs in its
// own process group; on timeout the whole group is killed so a background
// grandchild holding stderr cannot stall the caller.
func probeCanResume(agent *config.Agent, dir string, env []string, timeout time.Duration) (bool, error) {
	var stderr bytes.Buffer
	cmd := exec.Command(proc.Shell(), "-lc", agent.CanResume)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second

	fail := func(how string) error {
		msg := fmt.Sprintf("can_resume %s", how)
		if s := strings.TrimSpace(stderr.String()); s != "" {
			msg += ": " + s
		}
		return fmt.Errorf("%s (-R starts fresh without the check)", msg)
	}

	if err := cmd.Start(); err != nil {
		return false, fail("could not start: " + err.Error())
	}
	timedOut := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		close(timedOut)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	})
	err := cmd.Wait()
	timer.Stop()
	// Whatever the shell left behind (background children holding stderr)
	// dies with its group; ESRCH when nothing is left is fine.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-timedOut:
		return false, fail("timed out after " + timeout.String())
	default:
	}
	// Classify by the shell's own exit status, not by err: a grandchild that
	// outlives the shell makes Wait return ErrWaitDelay even after exit 0.
	st := cmd.ProcessState
	if st == nil {
		return false, fail("failed: " + err.Error())
	}
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return false, fail("killed by signal " + ws.Signal().String())
	}
	switch st.ExitCode() {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, fail(fmt.Sprintf("failed with exit %d", st.ExitCode()))
}

// agentCommandLine is the start or resume command with args appended quoted.
func agentCommandLine(agent *config.Agent, resume bool, args []string) string {
	line := agent.Start
	if resume {
		line = agent.Resume
	}
	for _, a := range args {
		line += " " + quoteArg(a)
	}
	return line
}

// requireIdentFirst enforces the shared first-positional rule of the two
// session commands: their identifier (workspace / task id) must come FIRST.
// With DisableFlagParsing a leading flag-looking token would silently become
// that identifier — `chaban chat --json T-1` resolving a workspace named
// "--json" and failing exit 3 "no workspace matching" — so it is classified
// here as what it is: a usage error (exit 2) that names the rule. The bare
// help forms are answered by the callers BEFORE this check, since -h is the
// one flag users legitimately type in that slot.
func requireIdentFirst(cmdName, noun string, args []string) error {
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return xerr.Wrap(xerr.ErrUsage,
			fmt.Errorf("chaban %s requires the %s as its first argument", cmdName, noun))
	}
	return nil
}

// runAgentSession is the session spawn, shared by `chat`, `launch` and `try`.
// It chooses start or resume (-R, or an agent without a resume command, means
// start; otherwise the can_resume probe decides, and a probe error aborts
// before anything starts), runs the command line as `$SHELL -lc` in ws.Dir with
// INHERITED stdio and sessionEnv, and turns the child's exit code into ours.
// For the session's duration it also names the terminal and tmux window after
// the workspace (setSessionTitle), restoring tmux's automatic naming on every
// exit path.
//
// SIGNALS: the agent owns the terminal, and the shell delivers ^C (SIGINT) and
// ^\ (SIGQUIT) to the whole foreground process group, so this parent receives
// them too. A parent that dies on the first ^C hands the shell its prompt back
// while the agent keeps drawing on the tty. So the parent absorbs both signals
// while the child runs and lets the child decide what ^C means.
//
// signal.Notify, NOT signal.Ignore: SIG_IGN survives exec and would make the
// child uninterruptible; Notify only changes this process, and Go resets it in
// a forked child. The channel is never read: absorbing is the whole job.
// signal.Stop restores the default once the session ends.
func runAgentSession(cfg *config.Config, ws wsp.Workspace, agent *config.Agent, noResume bool, args []string) error {
	env := sessionEnv(cfg, ws)
	resume := false
	if !noResume && strings.TrimSpace(agent.Resume) != "" {
		var err error
		if resume, err = probeCanResume(agent, ws.Dir, env, canResumeTimeout); err != nil {
			return err
		}
	}

	c := exec.Command(proc.Shell(), "-lc", agentCommandLine(agent, resume, args))
	c.Dir = ws.Dir
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	c.Env = env

	absorbed := make(chan os.Signal, 1)
	signal.Notify(absorbed, syscall.SIGINT, syscall.SIGQUIT)
	defer signal.Stop(absorbed)

	// After signal.Notify on purpose: by LIFO, restore() runs BEFORE
	// signal.Stop, so the rename is undone while ^C/^\ are still absorbed.
	restore := setSessionTitle(ws.Name())
	defer restore()

	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		// ExitCode() is -1 when the child died from a signal; that is not a
		// code to propagate, fall through to the plain exit-1 error.
		if errors.As(err, &ee) && ee.ExitCode() >= 0 {
			return xerr.Exit(ee.ExitCode())
		}
		return err
	}
	return nil
}

// sessionEnv composes the agent session's environment — the OTHER spawn
// contract (M4 plan, Global Constraints), distinct from wsp.CommandEnv's
// curated allowlist. Base: os.Environ(), i.e. the full inherited login env,
// already SanitizeSelf'd at startup so version-manager activation is gone.
// Overlay, winning per key: the workspace's resolved global env
// (wsp.ResolvedEnv — config `env` with runtime tokens substituted, no project)
// plus the runtime vars themselves (WORKSPACE and the numbered value vars,
// PORT0…), exported as real env vars — v1 exported both, and session-side
// scripts rely on $WORKSPACE/$PORT0 existing even when config `env` never
// mentions them. RuntimeVars is applied last: the tool-derived identity vars
// are authoritative over a config env entry that happens to reuse a name.
// PWD is overlaid with ws.Dir because the child runs with c.Dir = ws.Dir while
// the inherited PWD still names the launcher's cwd: children that trust $PWD
// instead of getcwd must see the session's actual working directory.
// Output is sorted for determinism, like envx.Curated.
func sessionEnv(cfg *config.Config, ws wsp.Workspace) []string {
	merged := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			merged[k] = v
		}
	}
	maps.Copy(merged, wsp.ResolvedEnv(cfg, ws.Alloc.TaskID, "", ws.Alloc.Index))
	maps.Copy(merged, wsp.RuntimeVars(cfg, ws.Alloc.TaskID, "", ws.Alloc.Index))
	merged["PWD"] = ws.Dir
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out
}
