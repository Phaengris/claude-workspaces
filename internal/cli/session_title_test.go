package cli

import "testing"

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"shorter than max", "T-1_x", 20, "T-1_x"},
		{"exactly max", "12345", 5, "12345"},
		{"over max", "T-2_abcdefghijklmnopqrstuvwxyz", 20, "T-2_abcdefghijklmnop"},
		{"empty", "", 20, ""},
		{"multibyte clamped on runes", "日本語のワークスペース", 5, "日本語のワ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateRunes(tc.in, tc.max); got != tc.want {
				t.Fatalf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

func TestOSCTitle(t *testing.T) {
	// Exact bytes: OSC 0 (icon+window title), BEL-terminated — the most
	// widely understood title sequence.
	if got, want := oscTitle("T-1_x"), "\x1b]0;T-1_x\x07"; got != want {
		t.Fatalf("oscTitle = %q, want %q", got, want)
	}
}
