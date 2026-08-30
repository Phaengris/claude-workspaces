package comm

import (
	"regexp"
	"time"
)

// The filters are pure functions over []Record — the whole window is a few
// hundred records at most, so an index would be slower than the loop. All of
// them preserve input order (oldest first).

// Unread selects what delivery hands a workspace (spec D8, D11): records
// STRICTLY newer than cursor, not older than Window relative to now (a
// record exactly Window old still qualifies — the same boundary Prune
// keeps), and not posted by self (a workspace is never told its own news).
func Unread(recs []Record, cursor time.Time, self string, now time.Time) []Record {
	panic("comm: Unread not implemented — this one is cat's")
}

// Since selects records STRICTLY newer than cutoff. It deliberately ignores
// Window — history stays searchable until it is physically pruned.
func Since(recs []Record, cutoff time.Time) []Record {
	panic("comm: Since not implemented — this one is cat's")
}

// Grep selects records whose From or Text matches re. Case-insensitivity is
// the caller's choice (compile with (?i) — the CLI does).
func Grep(recs []Record, re *regexp.Regexp) []Record {
	panic("comm: Grep not implemented — this one is cat's")
}

// Headline is the record's first line — what the over-HeadlinesOver
// fallback shows, and why the skill teaches headline-first announcements.
func Headline(r Record) string {
	panic("comm: Headline not implemented — this one is cat's")
}
