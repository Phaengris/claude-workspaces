package comm

import (
	"time"
)

// The cursor is a workspace's "delivered up to here" mark: the TS of the
// last record `get --new` handed to a session in that workspace. It lives at
// <wsDir>/.workspace/comm-cursor — the tool's per-workspace bookkeeping dir
// (pids-dir style): tool-owned, derived location, dies with the workspace,
// never in the registry (spec D6). One RFC3339Nano timestamp, one line.
//
// Sessions in one workspace share it; a lost race between two simultaneous
// sessions only re-delivers a record, so the cursor is written plainly
// (temp+rename, no lock).

// ReadCursor returns the workspace's cursor, or the zero time when the file
// is missing or unparsable — both mean "deliver the whole window", and the
// worst case of a corrupted cursor is one repeated delivery (doctor notes
// it; spec D14).
func ReadCursor(wsDir string) time.Time {
	panic("comm: ReadCursor not implemented — this one is cat's")
}

// WriteCursor records ts as the workspace's cursor, creating
// <wsDir>/.workspace/ if it does not exist yet (a fresh workspace may
// receive its first delivery before any daemon ever made the dir). Written
// via temp file + rename in the same directory.
func WriteCursor(wsDir string, ts time.Time) error {
	panic("comm: WriteCursor not implemented — this one is cat's")
}
