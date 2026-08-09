package cli

import "testing"

// TestDestroyExitCodes pins the codes destroy.txtar can only assert as
// "non-zero" (spec §9): an unresolvable workspace identifier is 3, and an
// arg-count violation is a usage error, 2, via usageArgs.
func TestDestroyExitCodes(t *testing.T) {
	cases := map[string]struct {
		args []string
		want int
	}{
		"unknown workspace": {args: []string{"destroy", "NOPE"}, want: 3},
		"no args":           {args: []string{"destroy"}, want: 2},
		"two args":          {args: []string{"destroy", "a", "b"}, want: 2},
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

// TestAssertInsideWorkspace pins the force-removal containment gate: nothing
// is ever removed unless the destination is STRICTLY inside the workspace dir,
// component-wise. The sibling-prefix trap (T-1_x vs T-1_xtra) and the
// same-dir case are the two ways a character-level check would go wrong.
func TestAssertInsideWorkspace(t *testing.T) {
	cases := map[string]struct {
		wsDir, dest string
		wantErr     bool
	}{
		"direct child":       {wsDir: "/root/T-1_x", dest: "/root/T-1_x/app", wantErr: false},
		"deep descendant":    {wsDir: "/root/T-1_x", dest: "/root/T-1_x/sub/dir", wantErr: false},
		"same dir":           {wsDir: "/root/T-1_x", dest: "/root/T-1_x", wantErr: true},
		"sibling prefix":     {wsDir: "/root/T-1_x", dest: "/root/T-1_xtra/app", wantErr: true},
		"parent":             {wsDir: "/root/T-1_x", dest: "/root", wantErr: true},
		"absolute escape":    {wsDir: "/root/T-1_x", dest: "/etc", wantErr: true},
		"escaping segments":  {wsDir: "/root/T-1_x", dest: "/root/T-1_x/../other", wantErr: true},
		"relative dest":      {wsDir: "/root/T-1_x", dest: "app", wantErr: true},
		"uncleaned interior": {wsDir: "/root/T-1_x", dest: "/root/T-1_x/./app", wantErr: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := assertInsideWorkspace(tc.wsDir, tc.dest)
			if (err != nil) != tc.wantErr {
				t.Errorf("assertInsideWorkspace(%q, %q) = %v, wantErr %v", tc.wsDir, tc.dest, err, tc.wantErr)
			}
		})
	}
}
