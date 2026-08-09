package alloc

import (
	"fmt"
	"sort"
)

// Allocate reserves dir for taskID and returns the new allocation. The whole
// read-modify-write cycle runs under WithLock, so two processes (or goroutines)
// racing to allocate cannot derive NextIndex from the same snapshot and hand
// out the same port block.
//
// Both keys are unique: a dir may hold one allocation, and a task id may name
// one workspace. Either collision is an error naming the dir that already
// exists, so the caller can say where to look instead of "already in use".
// Nothing is written when the allocation is rejected.
//
// now is the RFC 3339 creation timestamp, supplied by the caller so commands
// stay testable and a single run stamps one consistent time.
func Allocate(root, dir, taskID, desc, now string) (Allocation, error) {
	var a Allocation
	err := WithLock(root, func() error {
		reg, err := Load(root)
		if err != nil {
			return err
		}
		if existing, ok := reg[dir]; ok {
			return fmt.Errorf("%s is already allocated to task %s", dir, existing.TaskID)
		}
		// Sorted so the reported dir is stable if the registry were ever to
		// hold the same task id twice (it should not).
		for _, other := range sortedDirs(reg) {
			if reg[other].TaskID == taskID {
				return fmt.Errorf("task id %s is already allocated to %s", taskID, other)
			}
		}
		a = Allocation{
			Index:       NextIndex(reg),
			TaskID:      taskID,
			Description: desc,
			CreatedAt:   now,
		}
		reg[dir] = a
		return Save(root, reg)
	})
	if err != nil {
		return Allocation{}, err
	}
	return a, nil
}

// Release drops dir's allocation, freeing its index for reuse. An absent key is
// success, not an error: release is the cleanup half of destroy and must be
// safe to re-run after a partial failure.
func Release(root, dir string) error {
	return WithLock(root, func() error {
		reg, err := Load(root)
		if err != nil {
			return err
		}
		if _, ok := reg[dir]; !ok {
			return nil
		}
		delete(reg, dir)
		return Save(root, reg)
	})
}

// sortedDirs gives deterministic iteration over the registry's keys.
func sortedDirs(reg Registry) []string {
	dirs := make([]string, 0, len(reg))
	for dir := range reg {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}
