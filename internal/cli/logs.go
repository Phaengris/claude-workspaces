package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.internal/cat/claude-workspaces-go/internal/wsp"
	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// defaultLogLines is how much history `logs` prints without -n: the decided
// row's 50, i.e. about a screenful — enough to see why a daemon is unhappy,
// short enough to not bury the -f output that may follow.
const defaultLogLines = 50

// followInterval is the -f poll period (the decided row's 500ms). Polling, not
// inotify: it is portable, it costs one stat per file per half-second, and a
// log tail has no latency requirement worth a platform-specific watcher.
const followInterval = 500 * time.Millisecond

// newLogsCmd builds `workspace logs <workspace> <daemon> [-n N] [-f]`: print
// one daemon's stdout log, optionally streaming what comes next.
//
// The target must designate EXACTLY ONE daemon — a bare daemon name unique
// across configured projects, or an explicit `project:daemon`. It goes through
// the same wsp.ResolveTargets grammar as up/down/restart (so the names a user
// already types keep working) and is then narrowed: a PROJECT target is a
// usage error naming that project's daemons, because a project has as many
// logs as it has daemons and interleaving them would produce output no line of
// which can be traced back to a process. Unknown names keep resolution's exit
// 3, an ambiguous bare name keeps its exit 1.
//
// Nothing here requires the project to be checked out or the daemon to have
// ever run: `logs` is a read of a file whose path is derived from CONFIG. A
// daemon that exists but has no log yet is a note and exit 0 (the decided row)
// — "not started" is a legitimate answer to "show me the log", and scripts that
// poll for output should not have to special-case a failure code for it.
//
// Only the .log is printed. With -f, BOTH .log and .err.log are followed and
// their appended bytes are written to stdout raw and interleaved, unlabeled
// (the plan's non-goal: fancy multiplexing is out of scope for v1) — stderr is
// where a dying daemon explains itself, so a follow that showed only stdout
// would go quiet exactly when it matters. The .err.log's HISTORY is not
// printed, only what arrives after the follow starts: dumping it after the
// stdout tail would jumble old stderr into the new output with no ordering
// relationship to it.
func newLogsCmd() *cobra.Command {
	var (
		lines  int
		follow bool
	)
	cmd := &cobra.Command{
		Use:   "logs <workspace> <daemon>",
		Short: "Print one daemon's log, optionally following it",
		// Exactly the workspace and one target; anything else is a usage error
		// → exit 2 (spec §9).
		Args: usageArgs(cobra.ExactArgs(2)),
		// --json is inherited and deliberately unused: a log is bytes a
		// process wrote, not a structure this tool has any view of.
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, reg, err := loadRoot()
			if err != nil {
				return err
			}
			ws, err := wsp.Resolve(reg, args[0]) // ErrNotFound → exit 3
			if err != nil {
				return err
			}
			work, err := wsp.ResolveTargets(cfg, ws, args[1:2])
			if err != nil {
				return err // 3 unknown, 2 malformed, 1 ambiguous
			}
			d, err := soleDaemon(work, args[1])
			if err != nil {
				return err
			}
			// A nil Done channel (the default context) blocks forever in
			// select, which is exactly "-f until interrupted": the signal
			// terminates the process, nothing here has to catch it.
			return printLogs(cmd.OutOrStdout(), ws, d, lines, follow, cmd.Context().Done())
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", defaultLogLines, "print the last N lines (0 for none)")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new output as it arrives")
	return cmd
}

// soleDaemon narrows a resolved work list to the single daemon `logs` can act
// on. arg is the target as typed, so the refusal quotes what the user wrote.
//
// The only reachable rejection is a whole-project target (a project name, or a
// bare name that is both a project and a daemon — resolution gives the project
// priority). It is a USAGE error, not a not-found one: the name resolved fine,
// it just is not a daemon, and exit 3 would tell a script the workspace lacks
// something it actually has. The message lists the project's daemons as
// `project:daemon` keys — the exact strings that would work instead.
func soleDaemon(work []wsp.TargetWork, arg string) (wsp.Daemon, error) {
	// One argument yields exactly one entry; a different count means the
	// grammar changed under this command, which is a bug, not a user error.
	if len(work) != 1 {
		return wsp.Daemon{}, fmt.Errorf("target %q resolved to %d projects, want exactly one daemon", arg, len(work))
	}
	w := work[0]
	if w.WholeProject {
		if len(w.Daemons) == 0 {
			return wsp.Daemon{}, xerr.Wrap(xerr.ErrUsage,
				fmt.Errorf("target %q names project %q, which configures no daemons", arg, w.Project))
		}
		keys := make([]string, len(w.Daemons))
		for i, d := range w.Daemons {
			keys[i] = d.Key()
		}
		return wsp.Daemon{}, xerr.Wrap(xerr.ErrUsage,
			fmt.Errorf("target %q names project %q, not a daemon; pick one: %s",
				arg, w.Project, strings.Join(keys, ", ")))
	}
	if len(w.Daemons) != 1 {
		return wsp.Daemon{}, fmt.Errorf("target %q resolved to %d daemons, want exactly one", arg, len(w.Daemons))
	}
	return w.Daemons[0], nil
}

// printLogs writes the daemon's log tail and, with follow, keeps streaming.
// done stops the follow (nil = never, the command's own case); poll interval
// and stop channel are parameters so the follow can be driven in a test
// without a subprocess or a timeout.
func printLogs(w io.Writer, ws wsp.Workspace, d wsp.Daemon, lines int, follow bool, done <-chan struct{}) error {
	logPath := wsp.LogPath(ws, d)
	tail, size, err := readTail(logPath, lines)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// The daemon exists in config but has never written: say so, exit 0,
		// and still follow if asked — the file will appear when it starts.
		fmt.Fprintf(w, "no log yet for %s\n", d.Key())
	case err != nil:
		return err
	default:
		if _, err := w.Write(tail); err != nil {
			return err
		}
		// A daemon like python's http.server writes only to stderr, so an
		// empty stdout tail with a non-empty .err.log reads as "no output"
		// when there is plenty — one line points at where it went. Suppressed
		// under -f (which streams stderr anyway) and -n 0 (empty history was
		// explicitly requested); a fileSize error is ignored, the note is a
		// hint, never worth failing a successful tail over.
		if len(tail) == 0 && lines > 0 && !follow {
			if n, err := fileSize(wsp.ErrLogPath(ws, d)); err == nil && n > 0 {
				fmt.Fprintln(w, "(no stdout output; stderr has output — try -f)")
			}
		}
	}
	if !follow {
		return nil
	}
	// stdout resumes where the printed history ended; stderr starts at ITS
	// current end, so nothing already written there is replayed (see the
	// command's doc comment).
	errSize, err := fileSize(wsp.ErrLogPath(ws, d))
	if err != nil {
		return err
	}
	return followLogs(w, []logStream{
		{path: logPath, offset: size},
		{path: wsp.ErrLogPath(ws, d), offset: errSize},
	}, followInterval, done)
}

// readTail returns the last n lines of the file at path, plus the file's size
// — the offset a follow resumes from, captured in the same open so it can
// never be past what was printed.
//
// The file is read BACKWARDS in chunks rather than whole: a daemon log is
// unbounded between restarts (truncation happens at start, not by size), and
// `logs` must stay cheap on a log that has been growing all day.
//
// Each chunk is scanned ONCE and its boundary count carried into the next, so
// the work is linear in the bytes actually read. The obvious shape — prepend
// each chunk to a growing buffer and re-ask "does this hold n lines yet?" —
// rescans and recopies everything read so far on every iteration, which is
// quadratic in the tail; and with sparse newlines (a daemon printing very long
// lines, or none at all) that tail is the whole file.
func readTail(path string, n int) ([]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err // fs.ErrNotExist passes through: "no log yet"
	}
	defer f.Close()

	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, 0, err
	}
	if n <= 0 {
		// "No history" — what makes `-n 0 -f` mean "only what happens next".
		return nil, size, nil
	}
	const chunk = 64 << 10
	// Chunks newest-first, joined once at the end into exactly the tail.
	var rev [][]byte
	remaining := n
	for pos := size; pos > 0; {
		read := int64(chunk)
		if read > pos {
			read = pos
		}
		pos -= read
		b := make([]byte, read)
		if _, err := f.ReadAt(b, pos); err != nil {
			return nil, 0, err
		}
		// Only the FIRST chunk read is the file's end, where the terminating
		// newline belongs to the line it ends rather than starting another.
		cut, seen := tailScan(b, remaining, len(rev) == 0)
		remaining -= seen
		if cut >= 0 {
			rev = append(rev, b[cut:]) // n-th boundary found: the tail starts here
			break
		}
		rev = append(rev, b) // fewer than n lines so far — keep reading backwards
	}
	return joinReversed(rev), size, nil
}

// tailScan walks b backwards looking for n line boundaries. cut is the offset
// where the n-th-from-last line begins, or -1 when b holds fewer than n
// boundaries; seen is how many were found either way — the count the caller
// carries into the chunk BEFORE this one, which is what keeps the walk linear.
//
// atEnd says b ends the file, where the newline TERMINATING the final line is
// not a boundary of its own: it ends the line it belongs to. Skipping that
// subtlety is the classic off-by-one here — `-n 1` on "a\nb\n" would cut after
// the last byte and print nothing. An interior chunk gets no such discount:
// its last newline really does start the line that continues after it.
func tailScan(b []byte, n int, atEnd bool) (cut, seen int) {
	end := len(b)
	if atEnd && end > 0 && b[end-1] == '\n' {
		end--
	}
	for i := end - 1; i >= 0; i-- {
		if b[i] != '\n' {
			continue
		}
		seen++
		if seen == n {
			return i + 1, seen
		}
	}
	return -1, seen
}

// joinReversed concatenates chunks collected newest-first back into file
// order, in one allocation of exactly the right size.
func joinReversed(rev [][]byte) []byte {
	total := 0
	for _, b := range rev {
		total += len(b)
	}
	out := make([]byte, 0, total)
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i]...)
	}
	return out
}

// logStream is one file being followed and how far it has been consumed.
type logStream struct {
	path   string
	offset int64
}

// followLogs streams appended bytes from every stream to w until done fires,
// polling every interval. Output is raw and interleaved in stream order per
// poll — the plan's decision: labeling or merging stdout and stderr by time is
// out of scope, and the bytes a process wrote are what a user wants to see.
//
// A missing file is not an error at any point: it may not exist yet (a daemon
// that has not started) or may be replaced (a restart). Termination is the
// caller's: the command passes a channel that never fires, so `-f` runs until
// the process is interrupted.
func followLogs(w io.Writer, streams []logStream, interval time.Duration, done <-chan struct{}) error {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-done:
			// One final drain: bytes written just before the stop are still
			// the daemon's output, and dropping them would make the tail
			// depend on where the poll happened to land.
			return drainStreams(w, streams)
		case <-tick.C:
			if err := drainStreams(w, streams); err != nil {
				return err
			}
		}
	}
}

// drainStreams writes each stream's new bytes and advances its offset in
// place.
func drainStreams(w io.Writer, streams []logStream) error {
	for i := range streams {
		offset, err := drainStream(w, streams[i].path, streams[i].offset)
		if err != nil {
			return err
		}
		streams[i].offset = offset
	}
	return nil
}

// drainStream copies path[offset:] to w and returns the new offset.
//
// A file SHORTER than the offset was truncated — `up` truncates both logs at
// every start (spec §3) — so the offset resets to 0 and the new run is
// streamed from its beginning. The heuristic is size-based, so a restart that
// immediately writes more bytes than were consumed is read from the middle
// instead of the start; tracking inodes would fix that and is not worth the
// portability cost for a log tail.
func drainStream(w io.Writer, path string, offset int64) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return offset, nil // not written yet, or gone: keep waiting
		}
		return offset, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return offset, err
	}
	size := info.Size()
	if size < offset {
		offset = 0 // truncated: a new run starts at the beginning
	}
	if size == offset {
		return offset, nil
	}
	if _, err := io.Copy(w, io.NewSectionReader(f, offset, size-offset)); err != nil {
		return offset, err
	}
	return size, nil
}

// fileSize reports the size of path, treating a missing file as empty: a
// follow of a log that does not exist yet starts at 0 rather than failing.
func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
