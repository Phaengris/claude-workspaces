package comm

// The tests are the kata's contract (plan Task 1): they run red against the
// skeleton and define done for the hand-written bodies (Task 2). Boundary
// pins marked MUTATION-CHECKED are load-bearing: flipping the comparison
// they pin must fail them.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var base = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

func mustAppend(t *testing.T, s Store, from, text string, now time.Time) Record {
	t.Helper()
	r, err := s.Append(from, text, now)
	if err != nil {
		t.Fatalf("Append(%q, %q, %s): %v", from, text, now, err)
	}
	return r
}

func mustList(t *testing.T, s Store) []Record {
	t.Helper()
	recs, err := s.List(nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return recs
}

func texts(recs []Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Text
	}
	return out
}

func TestAppendStampsAndRoundTrips(t *testing.T) {
	root := t.TempDir()
	s := Open(root)

	r1 := mustAppend(t, s, "WS-1_alpha", "first headline\nand a body", base)
	if !r1.TS.Equal(base) {
		t.Errorf("first record on an empty board: TS = %s, want the given now %s", r1.TS, base)
	}
	if r1.From != "WS-1_alpha" || r1.Text != "first headline\nand a body" {
		t.Errorf("returned record does not carry the inputs: %+v", r1)
	}

	r2 := mustAppend(t, s, "WS-2_beta", "second", base.Add(time.Second))
	if !r2.TS.Equal(base.Add(time.Second)) {
		t.Errorf("second record: TS = %s, want %s", r2.TS, base.Add(time.Second))
	}

	recs := mustList(t, s)
	if len(recs) != 2 {
		t.Fatalf("List after two appends: %d records, want 2", len(recs))
	}
	for i, want := range []Record{r1, r2} {
		got := recs[i]
		if !got.TS.Equal(want.TS) || got.From != want.From || got.Text != want.Text {
			t.Errorf("record %d does not round-trip: got %+v, want %+v", i, got, want)
		}
	}
}

// MUTATION-CHECKED: `if !now.After(last)` — an implementation using
// now.Before(last) (or no bump at all) fails one of these two.
func TestAppendSameInstantBumpsByOneNanosecond(t *testing.T) {
	s := Open(t.TempDir())
	r1 := mustAppend(t, s, "A", "one", base)
	r2 := mustAppend(t, s, "B", "two", base) // same clock reading
	if want := r1.TS.Add(time.Nanosecond); !r2.TS.Equal(want) {
		t.Errorf("same-instant append: TS = %s, want last+1ns = %s", r2.TS, want)
	}
}

func TestAppendClockBackwardsBumps(t *testing.T) {
	s := Open(t.TempDir())
	r1 := mustAppend(t, s, "A", "one", base)
	r2 := mustAppend(t, s, "B", "two", base.Add(-time.Minute)) // clock stepped back
	if want := r1.TS.Add(time.Nanosecond); !r2.TS.Equal(want) {
		t.Errorf("clock-backwards append: TS = %s, want last+1ns = %s", r2.TS, want)
	}
}

func TestAppendPrunesOldRecordsInTheSameWrite(t *testing.T) {
	s := Open(t.TempDir())
	mustAppend(t, s, "A", "ancient", base)
	now := base.Add(Window + time.Hour)
	mustAppend(t, s, "B", "fresh", now)
	if got := texts(mustList(t, s)); len(got) != 1 || got[0] != "fresh" {
		t.Errorf("append did not prune: board holds %v, want [fresh]", got)
	}
}

func TestAppendConcurrentKeepsStrictOrder(t *testing.T) {
	s := Open(t.TempDir())
	var wg sync.WaitGroup
	for g := 0; g < 20; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				if _, err := s.Append("WS", "msg", time.Now()); err != nil {
					t.Errorf("concurrent Append: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	recs := mustList(t, s)
	if len(recs) != 100 {
		t.Fatalf("after 20×5 concurrent appends: %d records, want 100", len(recs))
	}
	for i := 1; i < len(recs); i++ {
		if !recs[i].TS.After(recs[i-1].TS) {
			t.Fatalf("TS not strictly increasing at %d: %s then %s", i, recs[i-1].TS, recs[i].TS)
		}
	}
}

func TestListSkipsCorruptLineAndWarnsOnce(t *testing.T) {
	root := t.TempDir()
	lines := `{"ts":"2026-08-30T12:00:00Z","from":"A","text":"one"}` + "\n" +
		`{this is not json` + "\n" +
		`{"ts":"2026-08-30T12:00:01Z","from":"B","text":"two"}` + "\n"
	if err := os.WriteFile(filepath.Join(root, ".comm.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	var warns []string
	recs, err := Open(root).List(func(m string) { warns = append(warns, m) })
	if err != nil {
		t.Fatalf("List over a corrupt line must not fail: %v", err)
	}
	if got := texts(recs); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("good neighbors lost: %v, want [one two]", got)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "line 2") {
		t.Errorf("warn = %v, want exactly one report naming line 2", warns)
	}
}

func TestListNilWarnIsSafe(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".comm.jsonl"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recs, err := Open(root).List(nil) // must not panic
	if err != nil || len(recs) != 0 {
		t.Errorf("List(nil) over garbage: recs=%v err=%v, want empty and nil", recs, err)
	}
}

func TestListMissingFileIsAnEmptyBoard(t *testing.T) {
	warned := false
	recs, err := Open(t.TempDir()).List(func(string) { warned = true })
	if err != nil || len(recs) != 0 || warned {
		t.Errorf("missing file: recs=%v err=%v warned=%v, want empty, nil, no warn", recs, err, warned)
	}
}

// MUTATION-CHECKED: the window boundary — Prune drops STRICTLY older than
// now-Window; a record exactly Window old survives. `<` flipped to `<=`
// fails this.
func TestPruneDropsOnlyStrictlyOlderThanWindow(t *testing.T) {
	s := Open(t.TempDir())
	now := base.Add(Window).Add(time.Nanosecond)
	mustAppend(t, s, "A", "too old", base)                             // age Window+1ns
	mustAppend(t, s, "B", "exactly window", base.Add(time.Nanosecond)) // age exactly Window
	mustAppend(t, s, "C", "fresh", base.Add(time.Hour))
	if err := s.Prune(now); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if got := texts(mustList(t, s)); len(got) != 2 || got[0] != "exactly window" || got[1] != "fresh" {
		t.Errorf("after Prune: %v, want [exactly window, fresh]", got)
	}
}

func TestWritesLeaveNoTempLitter(t *testing.T) {
	root := t.TempDir()
	s := Open(root)
	mustAppend(t, s, "A", "one", base)
	if err := s.Prune(base.Add(time.Hour)); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != ".comm.jsonl" && e.Name() != ".lock" {
			t.Errorf("unexpected file in root after writes: %s", e.Name())
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	ws := t.TempDir() // a bare workspace dir — no .workspace yet
	ts := base.Add(123 * time.Nanosecond)
	if err := WriteCursor(ws, ts); err != nil {
		t.Fatalf("WriteCursor on a fresh workspace (must create .workspace/): %v", err)
	}
	if got := ReadCursor(ws); !got.Equal(ts) {
		t.Errorf("cursor round trip: got %s, want %s (nanoseconds must survive)", got, ts)
	}
}

func TestCursorMissingIsZero(t *testing.T) {
	if got := ReadCursor(t.TempDir()); !got.IsZero() {
		t.Errorf("missing cursor: got %s, want zero time", got)
	}
}

func TestCursorGarbageIsZero(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".workspace", "comm-cursor"), []byte("not a timestamp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadCursor(ws); !got.IsZero() {
		t.Errorf("garbage cursor: got %s, want zero time", got)
	}
}

// MUTATION-CHECKED: both Unread boundaries — a record AT the cursor is not
// unread (strictly newer), and a record exactly Window old still is.
func TestUnread(t *testing.T) {
	now := base.Add(Window * 2)
	cursor := now.Add(-20 * time.Minute)
	recs := []Record{
		{TS: now.Add(-Window - time.Nanosecond), From: "OTHER", Text: "older than window"},
		{TS: now.Add(-30 * time.Minute), From: "OTHER", Text: "before cursor"},
		{TS: cursor, From: "OTHER", Text: "at cursor"},
		{TS: now.Add(-15 * time.Minute), From: "SELF", Text: "mine, newer than cursor"},
		{TS: now.Add(-10 * time.Minute), From: "OTHER", Text: "new one"},
		{TS: now.Add(-5 * time.Minute), From: "OTHER2", Text: "new two"},
	}
	got := texts(Unread(recs, cursor, "SELF", now))
	if len(got) != 2 || got[0] != "new one" || got[1] != "new two" {
		t.Errorf("Unread = %v, want [new one, new two] in order", got)
	}

	// Window boundary with a zero cursor: exactly-Window-old qualifies,
	// a nanosecond older does not.
	boundary := []Record{
		{TS: now.Add(-Window - time.Nanosecond), From: "OTHER", Text: "out"},
		{TS: now.Add(-Window), From: "OTHER", Text: "in"},
	}
	got = texts(Unread(boundary, time.Time{}, "SELF", now))
	if len(got) != 1 || got[0] != "in" {
		t.Errorf("window boundary: Unread = %v, want [in]", got)
	}
}

// MUTATION-CHECKED: Since is strictly-newer — the cutoff record itself is
// excluded — and ignores Window entirely.
func TestSince(t *testing.T) {
	now := base.Add(Window * 3)
	cutoff := now.Add(-Window * 2) // far outside the window: history stays searchable
	recs := []Record{
		{TS: cutoff.Add(-time.Hour), From: "A", Text: "before"},
		{TS: cutoff, From: "A", Text: "at cutoff"},
		{TS: cutoff.Add(time.Hour), From: "A", Text: "ancient but after"},
		{TS: now.Add(-time.Minute), From: "A", Text: "recent"},
	}
	got := texts(Since(recs, cutoff))
	if len(got) != 2 || got[0] != "ancient but after" || got[1] != "recent" {
		t.Errorf("Since = %v, want [ancient but after, recent]", got)
	}
}

func TestGrepMatchesFromAndText(t *testing.T) {
	recs := []Record{
		{TS: base, From: "PATED_editor-fixes", Text: "reworking CommandEnv\n- internal/wsp/env.go"},
		{TS: base.Add(time.Second), From: "TRY-3", Text: "scratch notes"},
		{TS: base.Add(2 * time.Second), From: "GESTALT_specs", Text: "commandenv consumers beware"},
	}
	re := regexp.MustCompile(`(?i)commandenv`)
	if got := texts(Grep(recs, re)); len(got) != 2 || !strings.Contains(got[0], "CommandEnv") || !strings.Contains(got[1], "commandenv") {
		t.Errorf("Grep over text = %v, want the two CommandEnv records in order", got)
	}
	re = regexp.MustCompile(`(?i)^try-`)
	if got := Grep(recs, re); len(got) != 1 || got[0].From != "TRY-3" {
		t.Errorf("Grep must match From too: got %v", texts(got))
	}
}

func TestHeadline(t *testing.T) {
	if got := Headline(Record{Text: "first line\nsecond\nthird"}); got != "first line" {
		t.Errorf("Headline = %q, want %q", got, "first line")
	}
	if got := Headline(Record{Text: "single"}); got != "single" {
		t.Errorf("Headline of one line = %q, want %q", got, "single")
	}
}
