package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Daemon primitives (spec §3): start a long-running user command detached in
// its own process group, record `<pid> <starttime>` for later liveness checks
// immune to pid recycling, and stop the whole group with TERM→KILL
// escalation. Linux-first: starttime comes from /proc/<pid>/stat; where /proc
// is unavailable (macOS) the recorded starttime is 0, which downgrades
// liveness to pid-only — a documented degradation, not an error.

// StartDaemon starts command as a daemon: `$SHELL -lc <command>` (SHELL from
// the CURRENT process env, fallback /bin/sh — same contract as Run) with
// cmd.Dir = dir and the caller-built TOTAL env (nil means EMPTY, as in Run).
// The child is placed in its OWN process group (Setpgid), so StopGroup can
// later signal the daemon and everything it forked. stdout goes to logPath
// and stderr to errPath, both opened O_CREATE|O_WRONLY|O_TRUNC 0644 (per-
// start truncation, spec §3) and closed in the parent after the start — the
// child keeps its own descriptors. On success pidPath is written with
// `<pid> <starttime>\n` (0644; parent directories are the caller's job) and
// the process handle is Release()d: nothing waits on the daemon, so its
// eventual death leaves a zombie for init (or, if the starter is still
// alive, an unreaped child — which is why Alive treats zombies as dead).
// If the pid file cannot be written, the just-started group is killed
// (best effort) and the error returned: an untracked daemon is worse than
// no daemon.
func StartDaemon(dir, command string, env []string, logPath, errPath, pidPath string) error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	if env == nil {
		env = []string{} // nil would make exec.Cmd inherit the raw parent env
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("opening daemon log: %w", err)
	}
	defer logFile.Close()
	errFile, err := os.OpenFile(errPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("opening daemon error log: %w", err)
	}
	defer errFile.Close()

	cmd := exec.Command(shell, "-lc", command)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = errFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}
	pid := cmd.Process.Pid

	starttime, err := Starttime(pid)
	if err != nil {
		starttime = 0 // /proc unreadable (non-Linux): pid-only degradation
	}
	content := fmt.Sprintf("%d %d\n", pid, starttime)
	if err := os.WriteFile(pidPath, []byte(content), 0o644); err != nil {
		_ = unix.Kill(-pid, unix.SIGKILL) // pgid == pid: don't leak an untracked daemon
		_ = cmd.Process.Release()
		return fmt.Errorf("writing pid file: %w", err)
	}
	return cmd.Process.Release()
}

// ReadPidFile parses pidPath's `<pid> <starttime>\n`. A missing file returns
// the os error unwrapped, so errors.Is(err, fs.ErrNotExist) holds (callers
// treat missing as "not running"). Any content that is not exactly a positive
// pid and an unsigned starttime is a corrupt-pid-file error — per the decided
// Liveness row, corrupt means "not running" upstream, but the parse itself
// reports what it saw.
func ReadPidFile(pidPath string) (pid int, starttime uint64, err error) {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, 0, err // fs.ErrNotExist passes through
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("corrupt pid file %s: want %q, got %q", pidPath, "<pid> <starttime>", strings.TrimSpace(string(data)))
	}
	pid, err = strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return 0, 0, fmt.Errorf("corrupt pid file %s: bad pid %q", pidPath, fields[0])
	}
	starttime, err = strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("corrupt pid file %s: bad starttime %q", pidPath, fields[1])
	}
	return pid, starttime, nil
}

// Starttime returns field 22 of /proc/<pid>/stat (starttime, clock ticks
// since boot). The comm field (2) is the executable basename and may contain
// spaces and parentheses, so fields are counted AFTER the LAST ')': the
// token right after it is field 3 (state), making starttime the 20th token
// after the parenthesis. On systems without /proc the read error is returned
// as-is (callers degrade to pid-only liveness).
func Starttime(pid int) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	_, starttime, err := parseStat(string(data))
	return starttime, err
}

// parseStat extracts state (field 3) and starttime (field 22) from a
// /proc/<pid>/stat line, counting after the last ')' per Starttime's note.
func parseStat(stat string) (state byte, starttime uint64, err error) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, 0, fmt.Errorf("malformed stat line: no ')'")
	}
	fields := strings.Fields(stat[i+1:])
	// fields[0] is field 3 (state); starttime is field 22 → fields[19].
	if len(fields) < 20 || len(fields[0]) != 1 {
		return 0, 0, fmt.Errorf("malformed stat line: %d fields after ')'", len(fields))
	}
	starttime, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("malformed stat starttime: %w", err)
	}
	return fields[0][0], starttime, nil
}

// Alive reports whether pid is the still-running process recorded as
// (pid, starttime). Existence is kill(pid, 0) succeeding or failing with
// EPERM (the process exists but isn't ours). Identity is the /proc
// starttime matching the recorded one — a recycled pid has a different
// starttime and reads dead; recorded starttime 0 skips the identity check
// (the documented non-Linux degradation, and the only case where an
// unreadable /proc still counts as alive). A zombie (state Z) is dead: it
// no longer runs, it merely awaits reaping by a parent that may be the very
// process asking (in-process test runs, restart flows).
func Alive(pid int, starttime uint64) bool {
	if pid <= 0 {
		return false // kill(0,·)/kill(-n,·) address groups, never a daemon
	}
	if err := unix.Kill(pid, 0); err != nil && !errors.Is(err, unix.EPERM) {
		return false
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return starttime == 0 // no /proc: pid-only degradation
	}
	state, live, err := parseStat(string(data))
	if err != nil {
		return starttime == 0
	}
	if state == 'Z' {
		return false
	}
	return starttime == 0 || live == starttime
}

// StopGroup stops the daemon recorded as (pid, starttime) and everything in
// its process group: SIGTERM to -pgid, poll Alive every 100ms for up to 5s;
// if still alive, SIGKILL to -pgid and poll up to 2s more; still alive after
// that is an error. Returns which signal sufficed ("TERM" or "KILL"), or
// ("", nil) when the process was not alive to begin with (idempotent no-op,
// including the process vanishing mid-call). StopGroup deliberately takes no
// pid-file path and NEVER touches the pid file: the CALLER removes it after
// StopGroup returns success (confirmed death) — that keeps file layout
// knowledge out of proc and lets a failed stop leave the record intact.
//
// Safety: signals are sent only when pid IS its group's leader (pgid == pid)
// — StartDaemon guarantees that for every daemon we start (Setpgid). A
// non-leader pid means the pid file is stale and the pid recycled (the
// starttime-0 degradation path cannot detect that itself), and signaling
// Getpgid's answer would hit an unrelated group: StopGroup errors instead,
// having signaled nothing.
//
// Limitation: the returned signal means the recorded LEADER is confirmed
// gone, not that every group member is — a member that ignores TERM can
// outlive a leader that exits on it (foreman-style trees). Group-emptiness
// polling (kill(-pgid, 0)) is deliberately NOT done: it counts zombies, so
// it would hang the in-process-parent case the zombie rule in Alive exists
// for. Callers reporting "stopped (TERM)" are promising leader death only.
func StopGroup(pid int, starttime uint64) (signal string, err error) {
	if !Alive(pid, starttime) {
		return "", nil
	}
	pgid, err := unix.Getpgid(pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return "", nil // vanished between the Alive check and here
		}
		return "", fmt.Errorf("getpgid(%d): %w", pid, err)
	}
	if pgid != pid {
		return "", fmt.Errorf("refusing to signal: pid %d is not a group leader — pid file may be stale", pid)
	}
	if err := unix.Kill(-pgid, unix.SIGTERM); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return "", nil
		}
		return "", fmt.Errorf("signaling group %d with SIGTERM: %w", pgid, err)
	}
	if waitGone(pid, starttime, 5*time.Second) {
		return "TERM", nil
	}
	if err := unix.Kill(-pgid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		return "", fmt.Errorf("signaling group %d with SIGKILL: %w", pgid, err)
	}
	if waitGone(pid, starttime, 2*time.Second) {
		return "KILL", nil
	}
	return "", fmt.Errorf("process %d (group %d) still alive after SIGKILL", pid, pgid)
}

// waitGone polls Alive every 100ms until it reports dead or budget expires.
func waitGone(pid int, starttime uint64, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if !Alive(pid, starttime) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}
