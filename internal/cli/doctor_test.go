package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/alloc"
	"git.internal/cat/claude-workspaces-go/internal/proc"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
)

// doctorStdout runs the CLI capturing stdout, for the doctor contracts that are
// about the OUTPUT rather than the exit code (runCLI discards both streams).
func doctorStdout(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	root := Root()
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out.String()
}

// writeFile writes content under dir, creating parents. Fixture plumbing for
// the doctor tests, which build a root by hand (doctor is read-only, so nothing
// has to be reachable through the creating commands).
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// livePidRecord is a pid file body naming THIS test process with its real
// starttime — a deterministically LIVE daemon record that spawns nothing and
// cannot outlive the test (the same trick gc_test.go's live case uses,
// including its pid-only degradation where /proc is unavailable).
func livePidRecord(t *testing.T) string {
	t.Helper()
	self := os.Getpid()
	st, err := proc.Starttime(self)
	if err != nil {
		st = 0
	}
	return strconv.Itoa(self) + " " + strconv.FormatUint(st, 10) + "\n"
}

// TestDoctorLiveDaemonsAreInformational pins the daemon-health check's
// informational half: live records are COUNTED, never flagged — a running
// daemon is the healthy state, so a workspace full of them still reports
// `no findings` (and exits 0). A live record under a key the config no longer
// names is still not a finding, but it IS noted on the count line: it is the
// one thing about it a user can act on (they cannot `workspace down` it by
// name), and doctor reports rather than fixes.
//
// Needs a real live process, which is why this is a Go test and not a txtar
// line: doctor_findings.txtar starts nothing.
func TestDoctorLiveDaemonsAreInformational(t *testing.T) {
	root := fixtureRoot(t, nil)
	repo := filepath.Join(root, "src", "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "config.yml"), doctorConfig(repo))
	wsDir := filepath.Join(root, "T-9_live")
	ws := wsp.Workspace{Dir: wsDir}
	writeFile(t, filepath.Join(wsp.PidsDir(ws), "app:sleeper"), livePidRecord(t))
	writeFile(t, filepath.Join(wsp.PidsDir(ws), "gone:ghost"), livePidRecord(t))
	if err := alloc.Save(root, alloc.Registry{wsDir: {Index: 0, TaskID: "T-9"}}); err != nil {
		t.Fatal(err)
	}

	out := doctorStdout(t, "doctor")
	if want := "daemons: T-9_live: 2 running (1 not in config)\n"; !strings.Contains(out, want) {
		t.Errorf("doctor output missing %q:\n%s", want, out)
	}
	if want := "doctor: no findings\n"; !strings.Contains(out, want) {
		t.Errorf("live daemons must not be findings; output:\n%s", out)
	}
}

// TestDoctorJSONShape pins the --json contract: the three documented top-level
// keys, both arrays present (empty, never null), findings and informational
// SPLIT by array rather than by kind, and the kind strings themselves — those
// are the machine handle a consumer branches on, so they are as much a contract
// as the human lines. detail is the human line verbatim, which is what keeps the
// two renderings from drifting.
func TestDoctorJSONShape(t *testing.T) {
	root := fixtureRoot(t, nil)
	// project "app"'s repo is deliberately NOT created: one missing-repo finding.
	writeFile(t, filepath.Join(root, "config.yml"), doctorConfig(filepath.Join(root, "src", "app")))
	live := filepath.Join(root, "T-1_live")
	gone := filepath.Join(root, "T-2_gone") // never created: stale allocation
	outside := filepath.Join(t.TempDir(), "A-9_adopted")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	ws := wsp.Workspace{Dir: live}
	writeFile(t, filepath.Join(wsp.PidsDir(ws), "app:sleeper"), livePidRecord(t))
	writeFile(t, filepath.Join(wsp.PidsDir(ws), "app:ghost"), "1 99999999999\n")
	if err := alloc.Save(root, alloc.Registry{
		live:    {Index: 0, TaskID: "T-1"},
		gone:    {Index: 1, TaskID: "T-2"},
		outside: {Index: 2, TaskID: "A-9", Adopted: true},
	}); err != nil {
		t.Fatal(err)
	}

	var rep doctorReport
	raw := doctorStdout(t, "doctor", "--json")
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatalf("doctor --json is not valid JSON (%v):\n%s", err, raw)
	}
	if rep.Config != "ok" {
		t.Errorf("config = %q, want %q", rep.Config, "ok")
	}
	wantFindings := map[string]string{
		kindStaleAllocation:    "T-2_gone",
		kindStalePidFile:       "T-1_live",
		kindProjectRepoMissing: "", // config-wide: no workspace field
	}
	got := map[string]string{}
	for _, f := range rep.Findings {
		if f.Detail == "" {
			t.Errorf("finding %+v has no detail", f)
		}
		got[f.Kind] = f.Workspace
	}
	for kind, wsName := range wantFindings {
		name, ok := got[kind]
		if !ok {
			t.Errorf("findings missing kind %q; got %+v", kind, rep.Findings)
			continue
		}
		if name != wsName {
			t.Errorf("finding %q workspace = %q, want %q", kind, name, wsName)
		}
	}
	if len(rep.Findings) != len(wantFindings) {
		t.Errorf("findings = %+v, want exactly %d", rep.Findings, len(wantFindings))
	}
	notes := map[string]bool{}
	for _, n := range rep.Informational {
		notes[n.Kind] = true
	}
	// The adopted out-of-root allocation and the live daemon are the two
	// informational entries: both are reported, NEITHER is a finding — which is
	// what keeps len(findings) at three above.
	for _, kind := range []string{kindAllocationOutsideRoot, kindDaemonsRunning} {
		if !notes[kind] {
			t.Errorf("informational missing kind %q; got %+v", kind, rep.Informational)
		}
	}
	// Field names are as much a contract as the kinds: pin the wire spellings.
	for _, want := range []string{`"config"`, `"findings"`, `"informational"`, `"kind"`, `"workspace"`, `"detail"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("doctor --json missing field %s:\n%s", want, raw)
		}
	}
}

// TestDoctorEmptyArraysNotNull pins that a clean root emits [] for both arrays.
// A nil slice marshals to null, which forces every consumer to special-case it;
// the txtar pins the bytes, this pins the reason.
func TestDoctorEmptyArraysNotNull(t *testing.T) {
	fixtureRoot(t, map[string]string{"config.yml": validConfig})
	raw := doctorStdout(t, "doctor", "--json")
	for _, want := range []string{`"findings": []`, `"informational": []`} {
		if !strings.Contains(raw, want) {
			t.Errorf("doctor --json missing %q:\n%s", want, raw)
		}
	}
}

// TestDoctorExitCodes pins the decided exit contract (spec §9): FINDINGS ARE
// NOT FAILURES — doctor reports, `gc`/`down` fix — so a root full of them still
// exits 0. Only the two pre-existing failures keep their codes: a config problem
// is 4 (the kind Load attaches, passed through untouched) and an unreadable
// registry is a plain error → 1. Extra positionals stay a usage error → 2
// (TestUsageErrorsExit2).
func TestDoctorExitCodes(t *testing.T) {
	t.Run("findings exit 0", func(t *testing.T) {
		root := fixtureRoot(t, nil)
		writeFile(t, filepath.Join(root, "config.yml"), doctorConfig(filepath.Join(root, "src", "app")))
		if err := alloc.Save(root, alloc.Registry{
			filepath.Join(root, "T-1_gone"): {Index: 0, TaskID: "T-1"},
		}); err != nil {
			t.Fatal(err)
		}
		if got := exitCodeFor(t, "doctor"); got != 0 {
			t.Errorf("doctor with findings exit code = %d, want 0", got)
		}
	})
	cases := map[string]struct {
		files map[string]string
		want  int
	}{
		"missing config":  {files: nil, want: 4},
		"invalid config":  {files: map[string]string{"config.yml": "projects:\n  app:\n    bogus_key: x\n"}, want: 4},
		"broken registry": {files: map[string]string{"config.yml": validConfig, ".allocations.json": "{ not json"}, want: 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fixtureRoot(t, tc.files)
			if got := exitCodeFor(t, "doctor"); got != tc.want {
				t.Errorf("doctor exit code = %d, want %d", got, tc.want)
			}
		})
	}
}

// doctorConfig configures one project, "app", whose only daemon is `sleeper` —
// so `app:sleeper` is a key the config knows and `gone:ghost` is not — with the
// given repo path. The path is passed in (and absolute) because doctor stats
// `repo` exactly as configured: a relative value would resolve against the test
// process's cwd, not the root.
func doctorConfig(repo string) string {
	return "values:\n  PORT: { start: 5000, per_workspace: 10 }\n" +
		"projects:\n  app:\n    repo: " + repo + "\n    start:\n      - sleeper: sleep 30\n"
}
