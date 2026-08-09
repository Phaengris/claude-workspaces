package wsp_test

import (
	"strings"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

func TestDirName(t *testing.T) {
	cases := []struct {
		taskID, desc, want string
	}{
		{"F-1", "Color Buttons!", "F-1_color-buttons"},
		{"F-1", "", "F-1"},
		{"F-1", "!!!", "F-1"},
		{"F-1", "a  b", "F-1_a-b"},
		{"F-1", "   ", "F-1"},
		{"FIZZY-123", "Fix the *login* flow", "FIZZY-123_fix-the-login-flow"},
		{"T.2", "already-slugged", "T.2_already-slugged"},
		{"T-3", "-leading and trailing-", "T-3_leading-and-trailing"},
		{"T-4", "v2.0 release", "T-4_v2-0-release"},
	}
	for _, tc := range cases {
		if got := wsp.DirName(tc.taskID, tc.desc); got != tc.want {
			t.Errorf("DirName(%q, %q) = %q, want %q", tc.taskID, tc.desc, got, tc.want)
		}
	}
}

func TestValidTaskID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"FIZZY-123", true},
		{"a", true},
		{"A1", true},
		{"T-1.2_x", true},
		{"9lives", true},
		{strings.Repeat("a", 64), true},
		{"", false},
		{".x", false},
		{"-x", false},
		{"_x", false},
		{"a/b", false},
		{"a b", false},
		{"a!", false},
		{strings.Repeat("a", 65), false},
	}
	for _, tc := range cases {
		if got := wsp.ValidTaskID(tc.id); got != tc.want {
			t.Errorf("ValidTaskID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}
