package alloc_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Phaengris/claude-workspaces/internal/alloc"
)

func TestLoadMissingFileIsEmptyRegistry(t *testing.T) {
	reg, err := alloc.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load on empty root: %v", err)
	}
	if len(reg) != 0 {
		t.Errorf("want empty registry, got %v", reg)
	}
}

func TestSaveLoadRoundtrip(t *testing.T) {
	root := t.TempDir()
	want := alloc.Registry{
		"/ws/FIZZY-123_fix":  {Index: 0, TaskID: "FIZZY-123", Description: "fix", CreatedAt: "2026-07-30T12:00:00+03:00"},
		"/elsewhere/adopted": {Index: 2, TaskID: "ADHOC-1", Adopted: true, CreatedAt: "2026-07-30T13:00:00+03:00"},
	}
	if err := alloc.Save(root, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := alloc.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got["/ws/FIZZY-123_fix"] != want["/ws/FIZZY-123_fix"] || got["/elsewhere/adopted"] != want["/elsewhere/adopted"] {
		t.Errorf("roundtrip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestSaveIsHiddenFileWithTrailingNewline(t *testing.T) {
	root := t.TempDir()
	if err := alloc.Save(root, alloc.Registry{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".allocations.json"))
	if err != nil {
		t.Fatalf("registry must live at <root>/.allocations.json: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("registry file should end with a newline")
	}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			t.Errorf("Save must not leave visible files behind, found %q", e.Name())
		}
	}
}

func TestWithLockExcludesSecondLocker(t *testing.T) {
	root := t.TempDir()
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- alloc.WithLock(root, func() error {
			close(locked)
			<-release
			return nil
		})
	}()
	// Guard against a broken implementation that errors out before running
	// fn: `locked` would never close, so a bare receive would hang the test.
	select {
	case <-locked: // the goroutine now holds the flock
	case err := <-done:
		t.Fatalf("WithLock returned without running fn: %v", err)
	}

	f, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// flock contends per open file description, so a second open in the same
	// process is a faithful stand-in for a second workspace process.
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
		t.Errorf("second locker should get EWOULDBLOCK while lock held, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("WithLock: %v", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Errorf("lock should be free after WithLock returns, got %v", err)
	}
}
