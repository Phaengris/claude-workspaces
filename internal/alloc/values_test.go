package alloc_test

import (
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/config"
)

func TestNextIndexFillsGaps(t *testing.T) {
	cases := []struct {
		name string
		used []int
		want int
	}{
		{"empty", nil, 0},
		{"contiguous", []int{0, 1, 2}, 3},
		{"gap", []int{0, 2, 3}, 1},
		{"zero freed", []int{1, 2}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := alloc.Registry{}
			for i, idx := range tc.used {
				reg[string(rune('a'+i))] = alloc.Allocation{Index: idx}
			}
			if got := alloc.NextIndex(reg); got != tc.want {
				t.Errorf("NextIndex(%v) = %d, want %d", tc.used, got, tc.want)
			}
		})
	}
}

func TestComputeValues(t *testing.T) {
	values := map[string]config.Value{
		"PORT":     {Start: 5000, PerWorkspace: 10},
		"REDIS_DB": {Start: 1, PerWorkspace: 1},
	}
	got := alloc.ComputeValues(values, 2)
	want := map[string]string{"PORT0": "5020", "PORT9": "5029", "REDIS_DB0": "3"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != 11 { // PORT0..PORT9 + REDIS_DB0
		t.Errorf("len = %d, want 11 (%v)", len(got), got)
	}
}
