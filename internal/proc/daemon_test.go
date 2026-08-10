package proc_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"git.internal/cat/claude-workspaces-go/internal/envx"
	"git.internal/cat/claude-workspaces-go/internal/proc"
)

// All daemon tests use REAL processes with bounded lifetimes (≤30s: every
// daemon is a `sleep 30` variant, self-expiring even if a cleanup path is
// missed). SHELL is pinned to /bin/sh (the developer's login shell may be
// fish) and HOME to a temp dir so `-lc` login init stays silent — the same
// idioms as proc_test.go. No mocks anywhere: process groups, signals and
// /proc parsing only prove themselves against the kernel.
//
// Reaping: the test binary is the daemons' parent and StartDaemon Release()s
// without waiting, so a dead daemon lingers as a zombie until reaped. Tests
// that assert kill-0 failure reap explicitly (unix.Wait4) first — mirroring
// production, where the starting CLI process has exited and init reaps.
// proc.Alive itself must treat zombies as dead (state Z in /proc), otherwise
// StopGroup could never confirm death when its caller is the parent.

// daemonEnv builds the curated child env used by every test daemon: default
// allowlist (PATH, HOME, ...) with HOME pointed at dir to silence login init.
func daemonEnv(dir string) []string {
	return envx.Curated(os.Environ(), nil, map[string]string{"HOME": dir})
}

// startDaemon starts command as a daemon in a fresh temp dir and returns the
// recorded pid/starttime plus all paths. Cleanup SIGKILLs the process group
// and reaps the leader so no zombies or stray sleeps outlive the test.
func startDaemon(t *testing.T, command string) (pid int, starttime uint64, dir, logPath, errPath, pidPath string) {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	dir = t.TempDir()
	logPath = filepath.Join(dir, "d.log")
	errPath = filepath.Join(dir, "d.err.log")
	pidPath = filepath.Join(dir, "d.pid")

	if err := proc.StartDaemon(dir, command, daemonEnv(dir), logPath, errPath, pidPath); err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}
	pid, starttime, err := proc.ReadPidFile(pidPath)
	if err != nil {
		t.Fatalf("ReadPidFile after StartDaemon: %v", err)
	}
	t.Cleanup(func() { killAndReap(pid) })
	return pid, starttime, dir, logPath, errPath, pidPath
}

// killAndReap best-effort SIGKILLs the group and reaps the direct child so
// the test binary accumulates no zombies. Errors are ignored: the group may
// already be gone and the child already reaped.
func killAndReap(pid int) {
	_ = unix.Kill(-pid, unix.SIGKILL) // pgid == pid: Setpgid made it leader
	for range 100 {
		wpid, err := unix.Wait4(pid, nil, unix.WNOHANG, nil)
		if err != nil || wpid == pid {
			return // reaped now, reaped earlier (ECHILD), or not our child
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// reap blocks until the direct child pid is reaped (it must already be dead
// or dying); after reap, kill(pid, 0) fails with ESRCH as in production.
func reap(t *testing.T, pid int) {
	t.Helper()
	if _, err := unix.Wait4(pid, nil, 0, nil); err != nil {
		t.Fatalf("Wait4(%d): %v", pid, err)
	}
}

// waitFor polls cond every 20ms up to timeout; fails the test on expiry.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartDaemonPidFileAndLiveness(t *testing.T) {
	pid, starttime, _, logPath, errPath, _ := startDaemon(t, "sleep 30")

	if pid <= 0 {
		t.Fatalf("pid file pid = %d, want > 0", pid)
	}
	if starttime == 0 {
		t.Errorf("recorded starttime = 0 on Linux; want the /proc value")
	}
	live, err := proc.Starttime(pid)
	if err != nil {
		t.Fatalf("Starttime(%d): %v", pid, err)
	}
	if live != starttime {
		t.Errorf("live starttime %d != recorded %d", live, starttime)
	}
	if !proc.Alive(pid, starttime) {
		t.Error("Alive = false for a freshly started daemon")
	}
	for _, p := range []string{logPath, errPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("log file %s not created: %v", p, err)
		}
	}
}

func TestStopGroupTerm(t *testing.T) {
	pid, starttime, _, _, _, _ := startDaemon(t, "sleep 30")

	sig, err := proc.StopGroup(pid, starttime)
	if err != nil {
		t.Fatalf("StopGroup: %v", err)
	}
	if sig != "TERM" {
		t.Errorf("StopGroup signal = %q, want TERM", sig)
	}
	if proc.Alive(pid, starttime) {
		t.Error("Alive = true after StopGroup confirmed death")
	}
	// Reap the zombie (production: init did this), then pin true absence.
	reap(t, pid)
	if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
		t.Errorf("kill(pid, 0) after reap = %v, want ESRCH", err)
	}
}

func TestStopGroupNotAliveIsNoop(t *testing.T) {
	pid, starttime, _, _, _, _ := startDaemon(t, "sleep 30")
	if _, err := proc.StopGroup(pid, starttime); err != nil {
		t.Fatalf("first StopGroup: %v", err)
	}

	sig, err := proc.StopGroup(pid, starttime)
	if err != nil {
		t.Fatalf("StopGroup on dead process: %v", err)
	}
	if sig != "" {
		t.Errorf("StopGroup on dead process signal = %q, want \"\" (no-op)", sig)
	}
}

func TestStopGroupKillsWholeGroup(t *testing.T) {
	// The daemon shell forks a background child, so the GROUP has members
	// beyond the leader; group death means kill(-pgid, 0) reports ESRCH.
	pid, starttime, _, _, _, _ := startDaemon(t, "sleep 30 & sleep 30")

	pgid, err := unix.Getpgid(pid)
	if err != nil {
		t.Fatalf("Getpgid(%d): %v", pid, err)
	}
	if pgid != pid {
		t.Errorf("pgid = %d, want %d (Setpgid must make the daemon its own group leader)", pgid, pid)
	}

	if _, err := proc.StopGroup(pid, starttime); err != nil {
		t.Fatalf("StopGroup: %v", err)
	}
	// Reap the leader; the background grandchild was reparented to init and
	// reaped there — poll until the whole group is gone.
	reap(t, pid)
	waitFor(t, 5*time.Second, "process group to vanish", func() bool {
		return errors.Is(unix.Kill(-pgid, 0), unix.ESRCH)
	})
}

func TestStopGroupKillEscalation(t *testing.T) {
	// The daemon ignores SIGTERM (`trap '' TERM` — SIG_IGN survives fork and
	// exec), so StopGroup must exhaust its 5s TERM budget and escalate.
	// This test takes ~5s wall time by design; it is the suite's single
	// escalation exercise (plan: acceptable once). The ready file closes a
	// race: StartDaemon returns before login init finishes, so signaling
	// immediately could catch the shell BEFORE the trap is installed.
	pid, starttime, dir, _, _, _ := startDaemon(t, "trap '' TERM; touch trap-ready; sleep 30")
	waitFor(t, 5*time.Second, "trap to be installed", func() bool {
		_, err := os.Stat(filepath.Join(dir, "trap-ready"))
		return err == nil
	})

	sig, err := proc.StopGroup(pid, starttime)
	if err != nil {
		t.Fatalf("StopGroup: %v", err)
	}
	if sig != "KILL" {
		t.Errorf("StopGroup signal = %q, want KILL for a TERM-ignoring daemon", sig)
	}
	if proc.Alive(pid, starttime) {
		t.Error("Alive = true after KILL escalation")
	}
}

func TestStopGroupRefusesNonLeader(t *testing.T) {
	// The starttime-0 degradation path skips the identity check, so a
	// recycled pid can read alive; StopGroup must then NOT trust Getpgid and
	// signal a stranger's group. Every daemon we start is its own group
	// leader (Setpgid), so pgid != pid proves the pid file is stale —
	// StopGroup must error and signal NOTHING. The victim group here is a
	// sacrificial leader+member pair (NOT the test's own group, so watching
	// the buggy behavior fail cannot nuke the harness).
	leader := exec.Command("sleep", "30")
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // own group, pgid == pid
	if err := leader.Start(); err != nil {
		t.Fatalf("starting leader: %v", err)
	}
	t.Cleanup(func() {
		_ = unix.Kill(-leader.Process.Pid, unix.SIGKILL)
		_ = leader.Wait()
	})
	member := exec.Command("sleep", "30")
	member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: leader.Process.Pid} // joins leader's group as NON-leader
	if err := member.Start(); err != nil {
		t.Fatalf("starting member: %v", err)
	}
	t.Cleanup(func() {
		_ = member.Process.Kill()
		_ = member.Wait()
	})

	sig, err := proc.StopGroup(member.Process.Pid, 0)
	if err == nil {
		t.Fatalf("StopGroup(non-leader, starttime 0) = (%q, nil), want refusal error", sig)
	}
	if !strings.Contains(err.Error(), "not a group leader") {
		t.Errorf("refusal error %q does not name the cause (not a group leader)", err)
	}
	// Nothing may have been signaled: both processes still run.
	if !proc.Alive(member.Process.Pid, 0) {
		t.Error("non-leader member was signaled; StopGroup must touch nothing on refusal")
	}
	if !proc.Alive(leader.Process.Pid, 0) {
		t.Error("group leader was signaled; StopGroup must touch nothing on refusal")
	}
}

func TestAliveRejectsRecycledStarttime(t *testing.T) {
	// A live pid with a WRONG recorded starttime models pid recycling: the
	// pid exists but is not the process the pid file described.
	pid := os.Getpid()
	real, err := proc.Starttime(pid)
	if err != nil {
		t.Fatalf("Starttime(self): %v", err)
	}
	if proc.Alive(pid, real+1) {
		t.Error("Alive = true for a live pid with mismatched starttime (recycled pid must read dead)")
	}
	if !proc.Alive(pid, real) {
		t.Error("Alive = false for a live pid with the correct starttime")
	}
	if !proc.Alive(pid, 0) {
		t.Error("Alive = false for starttime 0 (documented pid-only degradation must skip the check)")
	}
}

func TestStarttimeEvilComm(t *testing.T) {
	// comm (field 2 of /proc/<pid>/stat) is the executable basename and may
	// contain spaces and parentheses — the classic stat-parsing bug. Run a
	// copy of sleep under an evil name and exec it so the daemon pid IS the
	// evil binary, then Starttime must still parse the REAL file.
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	sleepBin, err := os.ReadFile("/usr/bin/sleep")
	if err != nil {
		t.Skipf("cannot read /usr/bin/sleep: %v", err)
	}
	evil := filepath.Join(dir, "ev) il (nm")
	if err := os.WriteFile(evil, sleepBin, 0o755); err != nil {
		t.Fatalf("writing evil sleep copy: %v", err)
	}

	logPath := filepath.Join(dir, "d.log")
	errPath := filepath.Join(dir, "d.err.log")
	pidPath := filepath.Join(dir, "d.pid")
	if err := proc.StartDaemon(dir, `exec './ev) il (nm' 30`, daemonEnv(dir), logPath, errPath, pidPath); err != nil {
		t.Fatalf("StartDaemon: %v", err)
	}
	pid, recorded, err := proc.ReadPidFile(pidPath)
	if err != nil {
		t.Fatalf("ReadPidFile: %v", err)
	}
	t.Cleanup(func() { killAndReap(pid) })

	// After exec the pid's comm contains ") (" — wait until the exec has
	// happened (comm no longer the shell's) before parsing.
	waitFor(t, 5*time.Second, "exec into evil binary", func() bool {
		comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		return err == nil && strings.Contains(string(comm), ")")
	})
	got, err := proc.Starttime(pid)
	if err != nil {
		t.Fatalf("Starttime with evil comm: %v", err)
	}
	if got != recorded {
		t.Errorf("Starttime = %d, want recorded %d (parsing must count after the LAST ')')", got, recorded)
	}
	if !proc.Alive(pid, recorded) {
		t.Error("Alive = false for live daemon with evil comm")
	}
}

func TestReadPidFileMissing(t *testing.T) {
	_, _, err := proc.ReadPidFile(filepath.Join(t.TempDir(), "absent.pid"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadPidFile(missing) error = %v, want errors.Is(fs.ErrNotExist)", err)
	}
}

func TestReadPidFileCorrupt(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"garbage":        "not a pid file\n",
		"missing field":  "1234\n",
		"non-num pid":    "12x 34\n",
		"non-num start":  "1234 9z9\n",
		"extra fields":   "12 34 56\n",
		"zero pid":       "0 34\n",
		"negative pid":   "-5 34\n",
		"negative start": "1234 -1\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "d.pid")
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := proc.ReadPidFile(p); err == nil {
				t.Errorf("ReadPidFile(%q) = nil error, want corrupt-content error", content)
			}
		})
	}
}

// TestStartDaemonPidFileFailureKillsGroup pins StartDaemon's last promise: if
// the pid file cannot be written, the group it JUST started is killed. An
// untracked daemon is worse than no daemon — nothing would ever find it again
// to stop it, and `up` would happily start a second one next to it.
//
// The forced failure is an unwritable pid directory (0500), which is exactly
// the production shape: the pid file's parent is the caller's job, so a bad
// mode or a read-only mount lands here. Root is skipped — it writes through
// any mode, so there would be no failure to observe.
//
// The death is observed by REAPING: the test binary is the daemon's parent
// (StartDaemon Release()s without waiting), so the killed child is a zombie
// only this process can collect, and its wait status carries the signal that
// killed it. Wait4(-1) is safe here because the package's tests are sequential
// and every other daemon is reaped by its own cleanup before the next test
// starts. Reaping is also the cleanup: no zombie outlives the test.
func TestStartDaemonPidFileFailureKillsGroup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0500 is still writable, so there is no failure to force")
	}
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	pidDir := filepath.Join(dir, "pids")
	if err := os.Mkdir(pidDir, 0o500); err != nil {
		t.Fatal(err)
	}
	// t.TempDir's cleanup must be able to remove pidDir's entries; restore a
	// normal mode even though nothing should have been created inside it.
	t.Cleanup(func() { _ = os.Chmod(pidDir, 0o700) })
	pidPath := filepath.Join(pidDir, "d.pid")

	err := proc.StartDaemon(dir, "sleep 30", daemonEnv(dir),
		filepath.Join(dir, "d.log"), filepath.Join(dir, "d.err.log"), pidPath)
	if err == nil {
		t.Fatal("StartDaemon with an unwritable pid dir = nil, want an error")
	}
	if !strings.Contains(err.Error(), "writing pid file") {
		t.Errorf("error %q does not name the failed step (writing pid file)", err)
	}
	if _, statErr := os.Stat(pidPath); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("stat(%s) = %v, want the pid file to be absent", pidPath, statErr)
	}

	var status unix.WaitStatus
	pid, waitErr := unix.Wait4(-1, &status, 0, nil)
	if waitErr != nil {
		t.Fatalf("Wait4 for the started daemon: %v (StartDaemon must leave a killed child behind)", waitErr)
	}
	if !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child %d wait status = %v (signaled=%v, signal=%v), want killed by SIGKILL",
			pid, status, status.Signaled(), status.Signal())
	}
	// Reaped: the pid is now truly absent, as it is in production once init
	// has collected it.
	if killErr := unix.Kill(pid, 0); !errors.Is(killErr, unix.ESRCH) {
		t.Errorf("kill(%d, 0) after reap = %v, want ESRCH", pid, killErr)
	}
}

func TestStartDaemonTruncatesLogs(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "d.log")
	errPath := filepath.Join(dir, "d.err.log")
	pidPath := filepath.Join(dir, "d.pid")
	env := daemonEnv(dir)

	// First start: the daemon writes a marker to stdout and stderr.
	if err := proc.StartDaemon(dir, "echo first-run-out; echo first-run-err >&2; sleep 30", env, logPath, errPath, pidPath); err != nil {
		t.Fatalf("first StartDaemon: %v", err)
	}
	pid1, st1, err := proc.ReadPidFile(pidPath)
	if err != nil {
		t.Fatalf("ReadPidFile: %v", err)
	}
	t.Cleanup(func() { killAndReap(pid1) })
	waitFor(t, 5*time.Second, "first run markers in logs", func() bool {
		out, _ := os.ReadFile(logPath)
		errOut, _ := os.ReadFile(errPath)
		return strings.Contains(string(out), "first-run-out") && strings.Contains(string(errOut), "first-run-err")
	})
	if _, err := proc.StopGroup(pid1, st1); err != nil {
		t.Fatalf("StopGroup: %v", err)
	}

	// Second start over the SAME paths must truncate both files.
	if err := proc.StartDaemon(dir, "sleep 30", env, logPath, errPath, pidPath); err != nil {
		t.Fatalf("second StartDaemon: %v", err)
	}
	pid2, _, err := proc.ReadPidFile(pidPath)
	if err != nil {
		t.Fatalf("ReadPidFile after second start: %v", err)
	}
	t.Cleanup(func() { killAndReap(pid2) })
	if pid2 == pid1 {
		t.Errorf("second start recorded the same pid %d; want a fresh process", pid2)
	}
	// O_TRUNC happens synchronously in StartDaemon, before it returns.
	out, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log after second start: %v", err)
	}
	if strings.Contains(string(out), "first-run-out") {
		t.Error("stdout log still contains the first run's marker; second start must truncate")
	}
	errOut, err := os.ReadFile(errPath)
	if err != nil {
		t.Fatalf("reading err log after second start: %v", err)
	}
	if strings.Contains(string(errOut), "first-run-err") {
		t.Error("stderr log still contains the first run's marker; second start must truncate")
	}
}
