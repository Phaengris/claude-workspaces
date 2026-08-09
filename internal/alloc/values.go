package alloc

import (
	"strconv"

	"git.internal/cat/claude-workspaces-go/internal/config"
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

// ComputeValues derives a workspace's numbered values from its index:
// NAME<n> = Start + index*PerWorkspace + n, for n in [0, PerWorkspace).
// Values are strings because they substitute into commands and env vars.
func ComputeValues(values map[string]config.Value, index int) map[string]string {
	out := make(map[string]string)
	for name, v := range values {
		base := v.Start + index*v.PerWorkspace
		for n := 0; n < v.PerWorkspace; n++ {
			out[name+strconv.Itoa(n)] = strconv.Itoa(base + n)
		}
	}
	return out
}
