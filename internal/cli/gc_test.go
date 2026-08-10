package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"git.internal/cat/claude-workspaces-go/internal/proc"
	"git.internal/cat/claude-workspaces-go/internal/wsp"
	"git.internal/cat/claude-workspaces-go/internal/xerr"
)

// TestGCExitCodes pins the codes gc.txtar can only assert as "non-zero" or
// not at all (spec §9): positionals are a usage error (2), a config problem
// keeps Load's kind (4), a broken registry is a plain error (1), and a root
// with nothing to collect — including a completely empty one — is success (0).
// The batch-failure exit (any pass error → 1, after the whole batch ran) is
// exercised in gc.txtar's join-and-continue section; its code is 1 because the
// joined per-workspace errors carry no xerr kind, the same plain-error rule
// the broken-registry row pins here.
func TestGCExitCodes(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		args  []string
		want  int
	}{
		"positional arg":             {files: map[string]string{"config.yml": validConfig}, args: []string{"gc", "extra"}, want: 2},
		"missing config":             {files: nil, args: []string{"gc"}, want: 4},
		"broken registry":            {files: map[string]string{"config.yml": validConfig, ".allocations.json": "{ not json"}, args: []string{"gc"}, want: 1},
		"nothing to do":              {files: map[string]string{"config.yml": validConfig}, args: []string{"gc"}, want: 0},
		"destroy-dirs on empty root": {files: map[string]string{"config.yml": validConfig}, args: []string{"gc", "--destroy-dirs"}, want: 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fixtureRoot(t, tc.files)
			if got := exitCodeFor(t, tc.args...); got != tc.want {
				t.Errorf("%v exit code = %d, want %d (spec §9)", tc.args, got, tc.want)
			}
		})
	}
}

// TestAnyDaemonRunningEnumeratesPidsDir pins the shared daemon gate's source of
// truth: the pids DIRECTORY, not the config. A daemon renamed or removed from
// config while running leaves a pid file under a key nothing configured can
// name, and the process it names still holds the workspace's ports — so
// `gc -d` must not destroy around it and `release` must not free its index.
// Reading the config could never see that file; reading the directory always
// does.
//
// The live case uses THIS test process (its real starttime, the pid-only
// degradation where /proc is unavailable) rather than spawning anything, so the
// liveness answer is deterministic and nothing outlives the test.
func TestAnyDaemonRunningEnumeratesPidsDir(t *testing.T) {
	ws := wsp.Workspace{Dir: t.TempDir()}
	pids := wsp.PidsDir(ws)

	// No pids directory at all: nothing runs here, and that is not an error —
	// a workspace that never ran `up` has no such directory.
	if running, err := anyDaemonRunning(ws); running || err != nil {
		t.Errorf("no pids dir = (%v, %v), want (false, nil)", running, err)
	}

	if err := os.MkdirAll(pids, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(key, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(pids, key), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A dead record and a corrupt one, both under keys no config knows: neither
	// is a running daemon (corrupt reads as dead, the decided Liveness row).
	write("gone:ghost", "1 99999999999\n")
	write("gone:corrupt", "garbage\n")
	if running, err := anyDaemonRunning(ws); running || err != nil {
		t.Errorf("dead + corrupt records = (%v, %v), want (false, nil)", running, err)
	}

	// A LIVE process under an unconfigured key blocks both callers.
	self := os.Getpid()
	st, err := proc.Starttime(self)
	if err != nil {
		st = 0 // no /proc: the documented pid-only degradation
	}
	write("gone:live", strconv.Itoa(self)+" "+strconv.FormatUint(st, 10)+"\n")
	if running, err := anyDaemonRunning(ws); !running || err != nil {
		t.Errorf("live record under an unconfigured key = (%v, %v), want (true, nil)", running, err)
	}
}

// TestFlattenBatchSeversKinds pins gc's batch-error rule (its doc comment's
// failure policy, M5 debt row): the joined per-workspace errors leave the
// command as a PLAIN error, so a kinded error raised deep inside one
// workspace's teardown — an xerr.ErrNotFound from some inner resolution, say —
// cannot drag the whole batch's exit code to 3 (or 4, or 2). Once several
// workspaces' failures are one error, a per-workspace code is meaningless;
// exit 1 is the honest answer, and the messages must all survive.
//
// The pin is on the helper with a SYNTHETIC joined error rather than on a
// txtar-built gc run: making a real pass raise a kinded failure means
// manufacturing an inner resolution error inside destroyWork, which no fixture
// can do without contorting the registry into a shape gc would reject earlier.
// The helper is where the severing lives, so the helper is what is pinned.
func TestFlattenBatchSeversKinds(t *testing.T) {
	inner := xerr.Wrap(xerr.ErrNotFound, errors.New("no such project \"gone\""))
	joined := errors.Join(
		fmt.Errorf("workspace A-1: %w", inner),
		errors.New("workspace B-2: teardown exploded"),
	)
	// Control: without the flattening the kind IS reachable, so the assertion
	// below tests the helper and not a fixture that never carried a kind.
	if !errors.Is(joined, xerr.ErrNotFound) {
		t.Fatal("fixture must carry the kind before flattening")
	}
	if got := xerr.ExitCode(joined); got != 3 {
		t.Fatalf("unflattened batch exit code = %d, want 3 (the kind leaking)", got)
	}

	flat := flattenBatch(joined)
	if errors.Is(flat, xerr.ErrNotFound) {
		t.Error("flattenBatch left the unwrap chain intact: a kinded inner error can still dictate the batch's exit code")
	}
	if got := xerr.ExitCode(flat); got != 1 {
		t.Errorf("flattened batch exit code = %d, want 1 (plain error)", got)
	}
	for _, want := range []string{"workspace A-1", "no such project", "workspace B-2", "teardown exploded"} {
		if !strings.Contains(flat.Error(), want) {
			t.Errorf("flattened message %q lost %q; severing the chain must not lose text", flat, want)
		}
	}
	if flattenBatch(nil) != nil {
		t.Error("flattenBatch(nil) must stay nil: a batch with no failures is a success")
	}
}
