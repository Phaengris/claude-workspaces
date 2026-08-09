package cli

import "testing"

// TestNewExitCodes pins the codes new.txtar can only assert as "non-zero"
// (spec §9). The order of checks is part of the contract: task-id validation
// (usage, 2) comes first, project validation (not found, 3) runs before any
// mutation, and a registry conflict (dup task id / dir) is a plain error (1).
func TestNewExitCodes(t *testing.T) {
	cases := map[string]struct {
		args []string
		want int
	}{
		"invalid task id":   {args: []string{"new", ".bad", "x"}, want: 2},
		"too-long task id":  {args: []string{"new", longTaskID(), "x"}, want: 2},
		"unknown project":   {args: []string{"new", "B-1", "x", "nope"}, want: 3},
		"duplicate task id": {args: []string{"new", "A-1", "different desc"}, want: 1},
		"missing args":      {args: []string{"new", "T-9"}, want: 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			registryRoot(t) // config with app/lib + allocation A-1 at <root>/A-1_x
			if got := exitCodeFor(t, tc.args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d (spec §9)", tc.args, got, tc.want)
			}
		})
	}
}

// longTaskID is one byte past the 64-byte limit — valid characters, invalid
// length, so it isolates the length half of ValidTaskID.
func longTaskID() string {
	b := make([]byte, 65)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
