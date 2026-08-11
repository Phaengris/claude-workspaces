package alloc

import (
	"strconv"

	"github.com/Phaengris/claude-workspaces/internal/config"
)

// NextIndex returns the lowest index not currently allocated. Gap-filling —
// a released workspace's index (and thus its port block) is reused instead
// of growing forever.
func NextIndex(reg Registry) int {
	used := make(map[int]bool, len(reg))
	for _, a := range reg {
		used[a.Index] = true
	}
	for i := 0; ; i++ {
		if !used[i] {
			return i
		}
	}
}

// Block returns the inclusive [first, last] numbers a value reserves for the
// workspace at index: first = Start + index*PerWorkspace, last = first +
// PerWorkspace - 1. This is the single home of the allocation arithmetic —
// ComputeValues names the individual numbers, `workspace ports` reports the
// range, and both must agree by construction rather than by coincidence.
// A PerWorkspace of 0 (rejected by config validation) yields last < first.
func Block(v config.Value, index int) (first, last int) {
	first = v.Start + index*v.PerWorkspace
	return first, first + v.PerWorkspace - 1
}

// ComputeValues derives a workspace's numbered values from its index:
// NAME<n> = Start + index*PerWorkspace + n, for n in [0, PerWorkspace).
// Values are strings because they substitute into commands and env vars.
func ComputeValues(values map[string]config.Value, index int) map[string]string {
	out := make(map[string]string)
	for name, v := range values {
		base, _ := Block(v, index)
		for n := 0; n < v.PerWorkspace; n++ {
			out[name+strconv.Itoa(n)] = strconv.Itoa(base + n)
		}
	}
	return out
}
