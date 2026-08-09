package alloc_test

import (
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
)

const testNow = "2026-08-09T12:00:00+03:00"

// mustAllocate is the happy path in one line; every failure here is a test bug,
// not an assertion about behaviour, so it fails loudly and immediately.
func mustAllocate(t *testing.T, root, dir, taskID, desc string) alloc.Allocation {
	t.Helper()
	a, err := alloc.Allocate(root, dir, taskID, desc, testNow)
	if err != nil {
		t.Fatalf("Allocate(%s, %s): %v", dir, taskID, err)
	}
	return a
}

func TestAllocateReleaseRoundTrip(t *testing.T) {
	root := t.TempDir()
	a := mustAllocate(t, root, "/ws/T-1", "T-1", "first task")

	want := alloc.Allocation{Index: 0, TaskID: "T-1", Description: "first task", CreatedAt: testNow}
	if a != want {
		t.Errorf("Allocate returned %+v, want %+v", a, want)
	}

	reg, err := alloc.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := reg["/ws/T-1"]; got != want {
		t.Errorf("persisted %+v, want %+v", got, want)
	}

	if err := alloc.Release(root, "/ws/T-1"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	reg, err = alloc.Load(root)
	if err != nil {
		t.Fatalf("Load after Release: %v", err)
	}
	if _, ok := reg["/ws/T-1"]; ok {
		t.Errorf("Release must remove the entry, registry still has %v", reg)
	}
}

func TestAllocateReusesReleasedIndex(t *testing.T) {
	root := t.TempDir()
	mustAllocate(t, root, "/ws/a", "A-1", "")
	b := mustAllocate(t, root, "/ws/b", "B-1", "")
	mustAllocate(t, root, "/ws/c", "C-1", "")
	if b.Index != 1 {
		t.Fatalf("second allocation index = %d, want 1", b.Index)
	}

	if err := alloc.Release(root, "/ws/b"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	d := mustAllocate(t, root, "/ws/d", "D-1", "")
	if d.Index != 1 {
		t.Errorf("index after release should be reused: got %d, want 1", d.Index)
	}
}

func TestReleaseUnknownDirIsNoOp(t *testing.T) {
	root := t.TempDir()
	mustAllocate(t, root, "/ws/a", "A-1", "")

	if err := alloc.Release(root, "/ws/ghost"); err != nil {
		t.Errorf("Release of an unallocated dir must be a no-op, got %v", err)
	}
	if err := alloc.Release(root, "/ws/a"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := alloc.Release(root, "/ws/a"); err != nil {
		t.Errorf("Release must be idempotent, second call got %v", err)
	}
	reg, err := alloc.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reg) != 0 {
		t.Errorf("registry should be empty, got %v", reg)
	}
}

func TestAllocateRejectsDuplicateDir(t *testing.T) {
	root := t.TempDir()
	mustAllocate(t, root, "/ws/T-1_thing", "T-1", "thing")

	_, err := alloc.Allocate(root, "/ws/T-1_thing", "OTHER-9", "other", testNow)
	if err == nil {
		t.Fatal("allocating an already-allocated dir must fail")
	}
	if !strings.Contains(err.Error(), "/ws/T-1_thing") {
		t.Errorf("error must name the existing dir, got %q", err)
	}

	reg, _ := alloc.Load(root)
	if len(reg) != 1 || reg["/ws/T-1_thing"].TaskID != "T-1" {
		t.Errorf("rejected Allocate must not touch the registry, got %v", reg)
	}
}

func TestAllocateRejectsDuplicateTaskID(t *testing.T) {
	root := t.TempDir()
	mustAllocate(t, root, "/ws/T-1_thing", "T-1", "thing")

	_, err := alloc.Allocate(root, "/ws/T-1_other", "T-1", "other", testNow)
	if err == nil {
		t.Fatal("allocating an already-allocated task id must fail")
	}
	if !strings.Contains(err.Error(), "/ws/T-1_thing") {
		t.Errorf("error must name the dir already holding the task id, got %q", err)
	}
	if !strings.Contains(err.Error(), "T-1") {
		t.Errorf("error must name the task id, got %q", err)
	}

	reg, _ := alloc.Load(root)
	if len(reg) != 1 {
		t.Errorf("rejected Allocate must not touch the registry, got %v", reg)
	}
}

// TestAllocateConcurrent proves the read-modify-write cycle is actually
// serialized: two goroutines allocating distinct dirs against the same root
// must not compute NextIndex from the same snapshot. flock is per open file
// description, so two goroutines in one process each opening .lock do block on
// each other — the same property M1's lock test relies on. Run with -race.
func TestAllocateConcurrent(t *testing.T) {
	root := t.TempDir()
	dirs := []string{filepath.Join(root, "T-1"), filepath.Join(root, "T-2")}

	var wg sync.WaitGroup
	got := make([]alloc.Allocation, len(dirs))
	errs := make([]error, len(dirs))
	for i := range dirs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = alloc.Allocate(root, dirs[i], "T-"+strconv.Itoa(i+1), "", testNow)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Allocate(%s): %v", dirs[i], err)
		}
	}
	if got[0].Index == got[1].Index {
		t.Errorf("concurrent allocations shared index %d — read-modify-write was not serialized", got[0].Index)
	}

	reg, err := alloc.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reg) != 2 {
		t.Fatalf("both allocations must survive, got %v", reg)
	}
	seen := map[int]bool{}
	for _, dir := range dirs {
		a, ok := reg[dir]
		if !ok {
			t.Fatalf("allocation for %s was lost: %v", dir, reg)
		}
		seen[a.Index] = true
	}
	if !seen[0] || !seen[1] {
		t.Errorf("persisted indices should be {0,1}, got %v", reg)
	}
}
