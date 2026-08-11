package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Phaengris/claude-workspaces/internal/wsp"
)

// daemonConfig is a config whose projects declare daemons, so the target
// grammar has something to resolve. `sleeper` is deliberately declared by BOTH
// projects (the ambiguity case) while `other` is unique to app.
const daemonConfig = `values:
  PORT: { start: 5000, per_workspace: 10 }
projects:
  app:
    repo: /tmp/app-src
    start:
      - sleeper: sleep 30
      - other: sleep 30
  lib:
    repo: /tmp/lib-src
    start:
      - sleeper: sleep 30
`

// daemonRoot builds a root holding daemonConfig plus one allocation at
// <root>/A-1_x. Nothing is checked out and no daemon has ever run: logs needs
// neither, which is exactly what these tests pin.
func daemonRoot(t *testing.T) string {
	t.Helper()
	root := fixtureRoot(t, map[string]string{"config.yml": daemonConfig})
	reg := `{"` + filepath.Join(root, "A-1_x") + `": {"index": 0, "task_id": "A-1", ` +
		`"description": "d", "created_at": "2026-08-01T09:00:00Z", "adopted": false}}`
	if err := os.WriteFile(filepath.Join(root, ".allocations.json"), []byte(reg), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// TestLogsExitCodes pins the codes status_logs.txtar can only assert as
// "non-zero" (spec §9). The interesting one is the SPLIT inside the target
// grammar: a name that does not exist is 3 (not found), while a name that
// exists but does not designate exactly one daemon — a project, a malformed
// pair — is 2 (usage): the user typed a real name at a place where only a
// daemon can be meant, which is a mistake about the command, not about the
// workspace. An ambiguous bare daemon name keeps ResolveTargets' plain error
// (1), unchanged by logs. A known daemon that never ran is exit 0: the note is
// output, not failure.
func TestLogsExitCodes(t *testing.T) {
	cases := map[string]struct {
		args []string
		want int
	}{
		"unknown workspace":      {args: []string{"logs", "NOPE-9", "other"}, want: 3},
		"unknown target":         {args: []string{"logs", "A-1", "nope"}, want: 3},
		"unknown daemon of pair": {args: []string{"logs", "A-1", "app:nope"}, want: 3},
		"malformed pair":         {args: []string{"logs", "A-1", "app:"}, want: 2},
		"project target":         {args: []string{"logs", "A-1", "app"}, want: 2},
		"ambiguous daemon name":  {args: []string{"logs", "A-1", "sleeper"}, want: 1},
		"no args":                {args: []string{"logs"}, want: 2},
		"workspace only":         {args: []string{"logs", "A-1"}, want: 2},
		"too many args":          {args: []string{"logs", "A-1", "other", "extra"}, want: 2},
		// Known daemon, no log file: the note is exit 0.
		"never ran, bare name": {args: []string{"logs", "A-1", "other"}, want: 0},
		"never ran, pair":      {args: []string{"logs", "A-1", "app:sleeper"}, want: 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			daemonRoot(t)
			if got := exitCodeFor(t, tc.args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d (spec §9)", tc.args, got, tc.want)
			}
		})
	}
}

// TestLogsProjectTargetNamesCandidates pins the refusal's content, not just its
// code: naming a project asks for several logs at once, and the fix is to pick
// one — so the message must list the daemons that were available to pick.
func TestLogsProjectTargetNamesCandidates(t *testing.T) {
	daemonRoot(t)
	err := runCLI(t, "logs", "A-1", "app")
	if err == nil {
		t.Fatal("logs on a project target succeeded, want a usage error")
	}
	for _, want := range []string{"app:sleeper", "app:other"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// TestTailScan pins the byte offset the tail is cut at, including the edges
// that make a naive implementation wrong: the newline TERMINATING the last
// line is not a line boundary of its own (so the last line is not counted
// twice), asking for more lines than a buffer holds reports "not found" plus
// how many were seen (that count is what carries across chunks), and a buffer
// that is NOT the file's end has no terminating newline to discount.
func TestTailScan(t *testing.T) {
	const log = "line1\nline2\nline3\n"
	cases := map[string]struct {
		in       string
		n        int
		atEnd    bool
		wantCut  int // offset into in, -1 when fewer than n boundaries exist
		wantSeen int
	}{
		"last one": {in: log, n: 1, atEnd: true, wantCut: 12, wantSeen: 1},
		"last two": {in: log, n: 2, atEnd: true, wantCut: 6, wantSeen: 2},
		// Three lines have only TWO boundaries once the terminating newline is
		// discounted: the first line starts where the buffer does, which is not
		// a boundary this scan can see — so "all of it" reports not-found, and
		// the caller keeps reading backwards until the file runs out.
		"exactly all":         {in: log, n: 3, atEnd: true, wantCut: -1, wantSeen: 2},
		"more than there are": {in: log, n: 50, atEnd: true, wantCut: -1, wantSeen: 2},
		// No terminating newline: the final partial line still counts as a line.
		"unterminated last line": {in: "a\nb", n: 1, atEnd: true, wantCut: 2, wantSeen: 1},
		"empty":                  {in: "", n: 5, atEnd: true, wantCut: -1, wantSeen: 0},
		// Interior chunk: every newline in it starts a line, the trailing one
		// included — the discount belongs to the file's end alone.
		"interior chunk":              {in: log, n: 1, atEnd: false, wantCut: 18, wantSeen: 1},
		"interior chunk, two":         {in: log, n: 2, atEnd: false, wantCut: 12, wantSeen: 2},
		"interior chunk, no boundary": {in: "abc", n: 1, atEnd: false, wantCut: -1, wantSeen: 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cut, seen := tailScan([]byte(tc.in), tc.n, tc.atEnd)
			if cut != tc.wantCut || seen != tc.wantSeen {
				t.Errorf("tailScan(%q, %d, %v) = (%d, %d), want (%d, %d)",
					tc.in, tc.n, tc.atEnd, cut, seen, tc.wantCut, tc.wantSeen)
			}
		})
	}
}

// referenceTail is the obvious, slow, whole-file implementation of "last n
// lines": split on newlines and keep the tail. It exists to be compared
// against, so readTail's backwards chunked walk — which never holds the whole
// file and carries its line count from chunk to chunk — has an independent
// definition of right, not just its own past output frozen into a golden file.
func referenceTail(content string, n int) string {
	if n <= 0 {
		return ""
	}
	body, trailing := content, ""
	if strings.HasSuffix(body, "\n") {
		body, trailing = body[:len(body)-1], "\n"
	}
	lines := strings.Split(body, "\n")
	if n < len(lines) {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + trailing
}

// TestReadTailMatchesReference is the multi-chunk pin for the chunked walk.
// readTail reads BACKWARDS in 64KiB chunks, so every interesting case is about
// what happens ACROSS a chunk boundary: a tail longer than one chunk, lines so
// sparse that a whole chunk contains no boundary at all, a file with no
// newline anywhere, and a newline sitting exactly on the boundary. All are
// compared byte-for-byte with referenceTail, and the returned size — the
// offset a follow resumes from — must always be the whole file.
func TestReadTailMatchesReference(t *testing.T) {
	const chunk = 64 << 10
	line := func(i int) string { return fmt.Sprintf("line %04d %s\n", i, strings.Repeat("x", 300)) }
	var manyLines strings.Builder // ~95KiB over several chunks
	for i := range 300 {
		manyLines.WriteString(line(i))
	}
	var sparse strings.Builder // 3 lines of ~80KiB: one line spans chunks
	for i := range 3 {
		sparse.WriteString(strings.Repeat(fmt.Sprintf("%d", i), 80<<10) + "\n")
	}

	fixtures := map[string]string{
		"empty":               "",
		"small":               "line1\nline2\nline3\n",
		"no trailing newline": strings.Repeat("a", chunk+7) + "\nsecond line, unterminated",
		"many lines":          manyLines.String(),
		"sparse newlines":     sparse.String(),
		"no newline at all":   strings.Repeat("z", 3*chunk+11),
		// A newline as the last byte of the first chunk read (the file's LAST
		// chunk), and another as the first byte of the previous one: the two
		// off-by-ones a chunk-carrying scan can hide.
		"newline on the boundary": strings.Repeat("p", chunk-1) + "\n" + strings.Repeat("q", chunk) + "\n",
		// A newline as the last byte of an INTERIOR chunk — the case that
		// separates "this chunk ends the file" from "this chunk ends here":
		// treating an interior chunk as the end discounts a real boundary and
		// returns one line too many. Chunks are cut from the END of the file,
		// so a size of exactly 2 chunks puts this newline at index chunk-1.
		"newline ends an interior chunk": strings.Repeat("a", chunk-1) + "\n" + strings.Repeat("b", chunk-1) + "\n",
	}
	for name, content := range fixtures {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app:x.log")
			write(t, path, content)
			for _, n := range []int{0, 1, 2, 3, 50, 1000} {
				got, size, err := readTail(path, n)
				if err != nil {
					t.Fatalf("readTail(n=%d): %v", n, err)
				}
				if size != int64(len(content)) {
					t.Errorf("readTail(n=%d) size = %d, want %d (the follow offset is the whole file)", n, size, len(content))
				}
				if want := referenceTail(content, n); string(got) != want {
					t.Errorf("readTail(n=%d) = %d bytes, want the reference's %d\n got tail: %q\nwant tail: %q",
						n, len(got), len(want), lastBytes(string(got), 40), lastBytes(want, 40))
				}
			}
		})
	}
}

// lastBytes trims a mismatch report to something readable — these fixtures run
// to hundreds of kilobytes.
func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// TestFollowLogs pins -f at the unit level, deliberately: `workspace logs -f`
// runs until interrupted, and a txtar script that backgrounds it has to guess
// how long to wait before killing it — a timing race in the suite. followLogs
// takes its poll interval and its stop channel as parameters precisely so the
// behavior can be driven deterministically here: the test writes, then closes
// done, and followLogs is called on the TEST's goroutine, so the buffer is read
// only after it has returned (no data race, nothing to synchronize).
//
// Both streams are covered — .log and .err.log are followed together — as is
// the truncation case, which is not exotic: `up` truncates both files at every
// start, so any follow that outlives a restart hits it.
func TestFollowLogs(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app:x.log")
	errPath := filepath.Join(dir, "app:x.err.log")
	write(t, logPath, "old\n")
	write(t, errPath, "olderr\n")

	// Follow from the end of what already exists: -f streams what comes NEXT,
	// the history having been printed already.
	streams := []logStream{
		{path: logPath, offset: 4},
		{path: errPath, offset: 7},
	}

	done := make(chan struct{})
	go func() {
		append_(t, logPath, "fresh\n")
		append_(t, errPath, "freshErr\n")
		time.Sleep(50 * time.Millisecond)
		// Truncate-and-rewrite, as a daemon restart does: the file gets SHORTER
		// than what has been consumed, and the offset must reset, or every later
		// byte is skipped as "already seen".
		write(t, logPath, "restart\n")
		time.Sleep(50 * time.Millisecond)
		close(done)
	}()

	var out bytes.Buffer
	if err := followLogs(&out, streams, time.Millisecond, done); err != nil {
		t.Fatalf("followLogs: %v", err)
	}
	got := out.String()
	for _, want := range []string{"fresh\n", "freshErr\n", "restart\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("followed output %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "old\n") || strings.Contains(got, "olderr\n") {
		t.Errorf("followed output %q replayed history before the start offset", got)
	}
}

// TestLogsFollowCommand covers the wiring the unit tests above cannot: the -f
// flag, the history-then-follow sequence, and the stop channel the command
// actually uses — cmd.Context().Done(), which is nil (never fires) in
// production and cancelable here. It stays fast despite the 500ms production
// poll because followLogs drains once more when it stops, so the appended
// bytes land whether or not a tick happened first.
//
// This is why -f is pinned in Go rather than in txtar: a script would have to
// background the command and guess when to kill it.
func TestLogsFollowCommand(t *testing.T) {
	root := daemonRoot(t)
	logDir := filepath.Join(root, "A-1_x", ".workspace", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, "app:other.log")
	write(t, logPath, "history\n")

	// The append is delayed so it lands while the command is already
	// following, and the line count is left at the default so the ASSERTION
	// does not depend on that: whether "appended" arrives through the tail or
	// through the follow, the output is the same two lines. A test that could
	// only pass one way would be a timing race in the suite.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		append_(t, logPath, "appended\n")
		cancel()
	}()

	cmd := Root()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"logs", "A-1", "other", "-f"})
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("logs -f: %v", err)
	}
	if got, want := out.String(), "history\nappended\n"; got != want {
		t.Errorf("logs -f output = %q, want %q (history, then what arrived)", got, want)
	}
}

// TestFollowLogsMissingFile pins that a stream whose file does not exist yet is
// not an error: `logs -f` on a daemon that has not started must sit there and
// pick the file up when it appears, exactly as it picks up appended bytes.
func TestFollowLogsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app:x.log")
	done := make(chan struct{})
	go func() {
		write(t, path, "born\n")
		time.Sleep(50 * time.Millisecond)
		close(done)
	}()

	var out bytes.Buffer
	if err := followLogs(&out, []logStream{{path: path}}, time.Millisecond, done); err != nil {
		t.Fatalf("followLogs: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "born\n") {
		t.Errorf("followed output %q missing the file that appeared", got)
	}
}

// write (over)writes a file. Errorf, never Fatalf: the follow tests call it
// from a writer goroutine, and FailNow off the test's own goroutine is not
// allowed. A failure here still fails the test — via the missing output.
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Errorf("write %s: %v", path, err)
	}
}

// append_ appends to an existing file (the trailing underscore keeps the
// builtin `append` usable in this file). Same goroutine rule as write.
func append_(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Errorf("open %s: %v", path, err)
		return
	}
	if _, err := f.WriteString(content); err != nil {
		t.Errorf("append %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Errorf("close %s: %v", path, err)
	}
}

// TestLogsEmptyStdoutNotesStderr pins the empty-stdout pointer note (M3 review
// take-along): a .log that exists but holds nothing, next to a .err.log that
// does, earns one hint line — and ONLY then. -n 0 asked for empty history,
// -f is about to stream stderr itself, and an empty .err.log means there is
// genuinely nothing to point at.
func TestLogsEmptyStdoutNotesStderr(t *testing.T) {
	const note = "(no stdout output; stderr has output — try -f)"
	newWS := func(t *testing.T, errContent string) (wsp.Workspace, wsp.Daemon) {
		t.Helper()
		ws := wsp.Workspace{Dir: t.TempDir()}
		d := wsp.Daemon{Project: "app", Name: "web"}
		if err := os.MkdirAll(filepath.Dir(wsp.LogPath(ws, d)), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, wsp.LogPath(ws, d), "")
		write(t, wsp.ErrLogPath(ws, d), errContent)
		return ws, d
	}
	closed := make(chan struct{})
	close(closed)

	cases := map[string]struct {
		errContent string
		lines      int
		follow     bool
		want       bool
	}{
		"empty stdout, stderr content": {errContent: "boom\n", lines: 50, want: true},
		"stderr also empty":            {errContent: "", lines: 50, want: false},
		"-n 0 asked for nothing":       {errContent: "boom\n", lines: 0, want: false},
		"-f streams stderr itself":     {errContent: "boom\n", lines: 50, follow: true, want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ws, d := newWS(t, tc.errContent)
			var out bytes.Buffer
			if err := printLogs(&out, ws, d, tc.lines, tc.follow, closed); err != nil {
				t.Fatalf("printLogs: %v", err)
			}
			if got := strings.Contains(out.String(), note); got != tc.want {
				t.Errorf("output %q: note present = %v, want %v", out.String(), got, tc.want)
			}
		})
	}
}
