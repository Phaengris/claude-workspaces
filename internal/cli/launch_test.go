package cli

import (
	"strings"
	"testing"
)

// TestLaunchExitCodes pins the codes the txtar script can only see as
// "non-zero" (spec §9). With DisableFlagParsing no cobra Args validator and no
// flag error can fire, so every one of these classifications is launch's own
// hand-written check — and the CREATE-vs-REUSE split decides which of them even
// applies (a missing description is only an error when creating).
func TestLaunchExitCodes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no task id at all", []string{"launch"}, 2},
		{"invalid task id", []string{"launch", "not a task id", "desc"}, 2},
		{"create without a description", []string{"launch", "T-9"}, 2},
		{"unknown project on create", []string{"launch", "T-9", "desc", "nosuch"}, 3},
		{"unknown project on reuse", []string{"launch", "T-1", "desc", "nosuch"}, 3},
		{"help in the task-id slot", []string{"launch", "--help"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claudeFixture(t, "#!/bin/sh\nexit 0\n")
			if got := exitCodeFor(t, tc.args...); got != tc.want {
				t.Errorf("exit code = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestSessionCommandsRejectLeadingFlag pins the shared first-positional rule of
// BOTH session commands (Task 1 review ruling): with flag parsing off, a
// flag-looking token in the workspace/task-id slot would otherwise become the
// identifier and fail as "no workspace matching \"--json\"" (exit 3) — a
// confusing code and a confusing message for what is plainly a usage mistake.
// It is a usage error (exit 2) naming the rule instead.
func TestSessionCommandsRejectLeadingFlag(t *testing.T) {
	for _, args := range [][]string{
		{"claude", "-S", "T-1"},
		{"claude", "--json", "T-1"},
		{"launch", "-S", "T-1"},
		{"launch", "--json", "T-1", "desc"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			claudeFixture(t, "#!/bin/sh\nexit 0\n")
			if got := exitCodeFor(t, args...); got != 2 {
				t.Errorf("exit code = %d, want 2 (usage)", got)
			}
			err := runCLI(t, args...)
			if err == nil {
				t.Fatal("expected a usage error")
			}
			if !strings.Contains(err.Error(), "first argument") {
				t.Errorf("error %q should name the first-argument rule", err)
			}
		})
	}
}

// TestLaunchExitCodePropagation pins that launch ends in the SAME session
// runner as `claude`: the child's code becomes ours, verbatim. T-1 exists in
// the fixture (reuse path) and the fixture config has no projects, so the
// up phase is a no-op success and the shim's 7 is the only thing left to
// report.
func TestLaunchExitCodePropagation(t *testing.T) {
	claudeFixture(t, "#!/bin/sh\nexit 7\n")
	if got := exitCodeFor(t, "launch", "T-1"); got != 7 {
		t.Errorf("exit code = %d, want 7 (the shim's own)", got)
	}
}

// TestSplitPassthrough pins the boundary launch needs and `claude` does not:
// everything before the first literal `--` is launch's own positional region,
// everything after it is claude's verbatim passthrough (the separator dropped,
// later `--`s kept). A nil post means "no separator was typed" and is
// deliberately distinct from an empty-but-present one only in that both yield
// no passthrough args.
func TestSplitPassthrough(t *testing.T) {
	cases := []struct {
		name string
		args []string
		pre  []string
		post []string
	}{
		{"no separator", []string{"T-1", "desc", "app"}, []string{"T-1", "desc", "app"}, nil},
		{"separator at the end", []string{"T-1", "desc", "--"}, []string{"T-1", "desc"}, nil},
		{"passthrough args", []string{"T-1", "d", "--", "-p", "hi"}, []string{"T-1", "d"}, []string{"-p", "hi"}},
		{"later separators belong to claude", []string{"T-1", "--", "-x", "--", "-y"}, []string{"T-1"}, []string{"-x", "--", "-y"}},
		{"separator first", []string{"--", "-p"}, nil, []string{"-p"}},
		{"empty", nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pre, post := splitPassthrough(tc.args)
			if !equalStrings(pre, tc.pre) || !equalStrings(post, tc.post) {
				t.Errorf("split = (%q, %q), want (%q, %q)", pre, post, tc.pre, tc.post)
			}
		})
	}
}

// equalStrings compares two string slices treating nil and empty as equal —
// the split's callers only ever range over the results.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
