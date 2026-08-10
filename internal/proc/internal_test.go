// White-box tests for the unexported /proc/<pid>/stat parser: the malformed
// inputs Starttime and Alive can only reach through a real /proc read, which a
// test cannot forge — the file is the kernel's. parseStat is where the shape
// assumptions live, so the table lives here (M4 debt row: parseStat malformed
// table via an internal test).
package proc

import (
	"strings"
	"testing"
)

// statLine builds a plausible /proc/<pid>/stat line: pid, comm, state, then
// `filler` further tokens before starttime. The real file has 50+ fields; only
// the first 22 matter here, so the tail is elided — parseStat counts FORWARD
// from ')' and never looks past field 22.
func statLine(comm, state string, filler int, starttime string) string {
	return "1234 (" + comm + ") " + state + " " + strings.Repeat("0 ", filler) + starttime
}

// TestParseStatMalformed pins that every shape parseStat cannot trust is an
// ERROR, never a silently wrong starttime: a wrong starttime is worse than a
// read failure, because Alive compares it and would call a recycled pid live.
// Callers degrade to pid-only liveness on error (documented), so the error
// itself is the contract.
func TestParseStatMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":                 "",
		"no close paren":        "1234 sleep S 0 0 0",
		"comm never closed":     "1234 (sleep S 0 0 0",
		"nothing after paren":   "1234 (sleep)",
		"too few fields":        statLine("sleep", "S", 17, "999"), // 19 after ')'
		"one field short":       statLine("sleep", "S", 18, ""),    // trailing space: 19 tokens
		"multi-byte state":      statLine("sleep", "SS", 18, "999"),
		"non-numeric starttime": statLine("sleep", "S", 18, "12x"),
		"negative starttime":    statLine("sleep", "S", 18, "-1"),
	}
	for name, stat := range cases {
		t.Run(name, func(t *testing.T) {
			state, starttime, err := parseStat(stat)
			if err == nil {
				t.Fatalf("parseStat(%q) = (%q, %d, nil), want an error", stat, state, starttime)
			}
			if !strings.Contains(err.Error(), "malformed stat") {
				t.Errorf("error %q does not identify the malformed stat line", err)
			}
			if state != 0 || starttime != 0 {
				t.Errorf("parseStat returned (%q, %d) alongside its error; a rejected line must yield nothing usable", state, starttime)
			}
		})
	}
}

// TestParseStatWellFormed is the positive control the table needs: without it,
// a parser that rejected EVERYTHING would pass. The evil-comm case is the
// reason fields are counted after the LAST ')' — an executable named with
// parentheses and spaces is legal, and counting after the FIRST ')' would read
// the wrong token as starttime.
func TestParseStatWellFormed(t *testing.T) {
	cases := map[string]struct {
		stat      string
		state     byte
		starttime uint64
	}{
		"plain comm":     {stat: statLine("sleep", "S", 18, "424242"), state: 'S', starttime: 424242},
		"zombie":         {stat: statLine("sleep", "Z", 18, "7"), state: 'Z', starttime: 7},
		"evil comm":      {stat: statLine("ev) il (nm", "R", 18, "99"), state: 'R', starttime: 99},
		"trailing tail":  {stat: statLine("sleep", "S", 18, "5 1 2 3 4\n"), state: 'S', starttime: 5},
		"leading spaces": {stat: " " + statLine("sleep", "D", 18, "6"), state: 'D', starttime: 6},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			state, starttime, err := parseStat(tc.stat)
			if err != nil {
				t.Fatalf("parseStat(%q): %v", tc.stat, err)
			}
			if state != tc.state {
				t.Errorf("state = %q, want %q", state, tc.state)
			}
			if starttime != tc.starttime {
				t.Errorf("starttime = %d, want %d", starttime, tc.starttime)
			}
		})
	}
}
