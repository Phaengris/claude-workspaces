// Package alloc owns <root>/.allocations.json — the tool's ONLY registry
// (spec §3). Everything else about a workspace is derived from reality.
package alloc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	registryName = ".allocations.json"
	lockName     = ".lock"
)

// Allocation records the facts that cannot be derived by looking at a
// workspace dir: its index (the basis for PORT0…) and its identity.
type Allocation struct {
	Index       int    `json:"index"`
	TaskID      string `json:"task_id"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"` // RFC 3339
	Adopted     bool   `json:"adopted"`
}

// Registry maps absolute workspace dir → allocation.
type Registry map[string]Allocation

// Load reads the registry; a missing file is an empty registry, not an error
// (a fresh root has no allocations yet).
func Load(root string) (Registry, error) {
	data, err := os.ReadFile(filepath.Join(root, registryName))
	if errors.Is(err, fs.ErrNotExist) {
		return Registry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var reg Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", registryName, err)
	}
	return reg, nil
}

// Save writes the registry atomically: temp file in the same dir, fsync,
// rename, fsync the dir. A reader never observes a half-written file, even
// without the lock, and a completed Save survives a crash.
//
// Note the one asymmetry: an error from the closing directory fsync is
// returned after the rename has already taken effect, so a failed Save may
// still have replaced the registry — the new content is visible and merely of
// uncertain durability. Callers should treat a Save error as "state may have
// changed" and re-read rather than assume the old registry stands.
func Save(root string, reg Registry) error {
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(root, ".allocations-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(root, registryName)); err != nil {
		return err
	}
	// The rename itself is only durable once the directory is synced; the
	// registry is the tool's only persistent state, so pay for the fsync.
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// WithLock runs fn while holding an exclusive flock on <root>/.lock —
// the mutual exclusion for read-modify-write cycles across workspace
// processes. Advisory: correctness relies on every writer using WithLock.
func WithLock(root string, fn func() error) error {
	f, err := os.OpenFile(filepath.Join(root, lockName), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("locking %s: %w", lockName, err)
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)

	return fn()
}
