// Package comm owns <root>/.comm.jsonl — the announcements board (spec:
// docs/superpowers/specs/2026-08-30-workspace-comm-design.md). One board per
// workspaces root; every workspace posts to it and reads all of it.
//
// The board carries ANNOUNCEMENTS — rare, imperative, a headline plus
// bullets — never conversation. Delivery is the hooks' job (they may record
// what they delivered — the per-workspace cursor — and never create, start
// or modify a workspace); relevance is the reading agent's judgment, not a
// filter's.
//
// Everything here works on values and returns errors; formatting lives in
// internal/cli, locking comes from internal/alloc (the same <root>/.lock the
// registry uses).
package comm

import (
	"time"
)

// Record is one announcement.
//
// TS is RFC 3339 with nanoseconds and STRICTLY increasing within a board:
// Append enforces it under the lock, so a record's timestamp is a total
// order and a cursor (a timestamp) can never be invalidated by pruning —
// integer ids would restart after a full prune and silently swallow
// deliveries (spec D3).
type Record struct {
	TS   time.Time `json:"ts"`
	From string    `json:"from"` // the posting workspace's name
	Text string    `json:"text"` // freeform; by convention the first line is the headline
}

// Window is how long an announcement lives (spec D5): reads consider only
// records younger than this, and writes prune records older than it. A
// week-old change is usually merged into the base a reader's branch already
// sits on; 30 days comfortably covers a task's lifetime.
const Window = 30 * 24 * time.Hour

// MaxText is put's size guard (spec D13): every announcement lands in every
// sibling session's context, so an announcement is a headline and bullets —
// never a diff.
const MaxText = 4096

// HeadlinesOver is delivery's overload guard (spec D8): when more than this
// many records qualify as unread, delivery shows headlines only.
const HeadlinesOver = 10

// Store is the board. The JSONL file is one implementation; nothing above
// this interface knows about files (spec D17 — the seam a future database
// slots in behind).
type Store interface {
	// Append posts text as from, stamped now — or last+1ns when now is not
	// after the newest stored record (same-instant posts, clocks stepping
	// backwards), so TS stays strictly increasing. It prunes records older
	// than Window in the same write. The whole read-modify-write cycle runs
	// under the root lock, and the file is replaced atomically. Returns the
	// record as stored.
	Append(from, text string, now time.Time) (Record, error)

	// List returns every parseable record, oldest first (file order). A
	// malformed line is skipped, reported through warn — "line <n>: <reason>"
	// — and never fatal: a bricked board would fail every hook and every
	// put (spec D14). warn may be nil (reports are dropped). A missing file
	// is an empty board, not an error.
	List(warn func(string)) ([]Record, error)

	// Prune drops records older than now-Window (strictly older: a record
	// exactly Window old survives). Runs under the root lock, atomic
	// replace, like Append.
	Prune(now time.Time) error
}
