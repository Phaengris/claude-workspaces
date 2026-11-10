package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/Phaengris/chaban/v2/internal/config"
	"github.com/Phaengris/chaban/v2/internal/proc"
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
// literal word: each ' becomes '\” and each \ becomes '\\' (close the quote,
// escaped char, reopen).
func quoteArg(s string) string {
	return "'" + quoteArgEscaper.Replace(s) + "'"
}

// resolveAgent returns the configured agent, or the built-in Claude block when
// config has none and claude is on chaban's PATH (noting that on stderr).
func resolveAgent(cfg *config.Config, stderr io.Writer) (*config.Agent, error) {
	if cfg.Agent != nil {
		return cfg.Agent, nil
	}
	fallback := config.ClaudeAgent()
	prog := strings.Fields(fallback.Start)[0]
	if _, err := exec.LookPath(prog); err != nil {
		return nil, xerr.Wrap(xerr.ErrConfig, fmt.Errorf(
			"no agent: block in config.yml and no %s on PATH; add one, for example:\n\n%s",
			prog, config.ClaudeAgentYAML()))
	}
	fmt.Fprintf(stderr, "chaban: no agent: in config.yml, starting %s (found on PATH); add an agent: block to choose\n", prog)
	return fallback, nil
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
	select {
	case <-timedOut:
		return false, fail("timed out after " + timeout.String())
	default:
	}
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ee.ExitCode() == 1 {
			return false, nil
		}
		return false, fail(fmt.Sprintf("failed with exit %d", ee.ExitCode()))
	}
	return false, fail("failed: " + err.Error())
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
