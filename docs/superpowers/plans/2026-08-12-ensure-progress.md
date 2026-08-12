# Ensure-Chain Progress + Launch Hint Split Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The ensure chain reports each slow step (worktree checkout, every
setup command) as a live, duration-stamped line; `launch` replaces the
misleading `hint: workspace cd` with a parallel-terminal tip on its create
path while `new` keeps the hint.

**Architecture:** `wsp` gains a tiny reporter type (`Step`) threaded through
`EnsureProject`; `nil` = today's silence. Formatting lives in one cli
printer (`projectStepper`) that `new`/`checkout`/`up` (and launch through
them) hand in. The hint moves out of `newWork` to the command layer so the
two commands can differ.

**Tech Stack:** Go stdlib only. testscript txtar.

**Spec:** `docs/superpowers/specs/2026-08-12-ensure-progress-design.md` —
its Decided-behaviors table binds every task.

## Global Constraints

- Clean-room: never consult Ruby v1 code. Spec + this repo only.
- Conventional commits; every commit ends with the two trailer lines used by
  every commit on this branch (copy from `git log -1 --format=%B`).
- `gofmt -l .` (prints nothing) and `go vet ./...` clean before every commit;
  full `go test ./...` green. TDD: failing test first. Mutation-check
  load-bearing pins and say so in the commit body.
- README changes in the SAME commit as the behavior they document.
- Spec row 4: plain text only — no spinners, no control characters, no TTY
  gating. Spec row 6: the existing summary lines (`workspace <name> created
  (index N)`, `  <project>: checked out …`, `started <key> (pid N)`) are
  UNCHANGED; seven txtars run setup (new, up, launch, destroy, checkout,
  status_logs, status_env) and every pre-existing pin must keep passing.
- Output-order fact both tasks must respect: on a fresh `new`/`launch`
  create, progress lines print BEFORE `workspace <name> created (index N)` —
  creation is transactional and the created line states the committed result.
- Work on branch `feat/ensure-progress` off master; push only at release.

---

### Task 1: `wsp.Step` — the reporter, threaded through EnsureProject

**Files:**
- Modify: `internal/wsp/ensure.go` (EnsureProject, ~lines 33-96)
- Modify: `internal/wsp/ensure_test.go` (new behavior test; read the file
  first and reuse its fixture helpers — it already builds hermetic git repos)
- Modify (compile only, `nil` argument): `internal/cli/new.go:237`,
  `internal/cli/checkout.go:63`, `internal/cli/up.go:120`

**Interfaces:**
- Produces: `type Step func(label string) (done func(err error))` in
  `internal/wsp`, and the new signature
  `EnsureProject(cfg *config.Config, ws Workspace, project string, step Step) error`.
  Task 2 passes a real Step from cli; until then every caller passes `nil`.

- [ ] **Step 1: Write the failing test**

In `internal/wsp/ensure_test.go` (adapt fixture construction to the file's
existing helpers — the BEHAVIORS pinned are what matters):

```go
// recordingStep collects "begin <label>" / "end <label> ok|err" markers in
// call order — the test double for the progress reporter.
func recordingStep(log *[]string) wsp.Step {
	return func(label string) func(error) {
		*log = append(*log, "begin "+label)
		return func(err error) {
			verdict := "ok"
			if err != nil {
				verdict = "err"
			}
			*log = append(*log, "end "+label+" "+verdict)
		}
	}
}

func TestEnsureProjectReportsSteps(t *testing.T) {
	// Fixture: one project with two setup commands, one of which carries a
	// ${} token so the test pins that labels show the SUBSTITUTED command.
	//   setup: ["true", "echo ${WORKSPACE}"]
	// Build cfg/ws with the file's existing helpers (real repo, real
	// worktree destination under t.TempDir()).

	var log []string
	if err := wsp.EnsureProject(cfg, ws, "app", recordingStep(&log)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"begin checking out (branch " + ws.Alloc.TaskID + ")",
		"end checking out (branch " + ws.Alloc.TaskID + ") ok",
		"begin setup: true",
		"end setup: true ok",
		"begin setup: echo " + ws.Alloc.TaskID,
		"end setup: echo " + ws.Alloc.TaskID + " ok",
	}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("first run log:\n got %q\nwant %q", log, want)
	}

	// Idempotent re-run: worktree exists, stamp current → COMPLETE silence
	// (spec row 1 + Mechanics: steps report only when they actually run).
	log = nil
	if err := wsp.EnsureProject(cfg, ws, "app", recordingStep(&log)); err != nil {
		t.Fatal(err)
	}
	if len(log) != 0 {
		t.Fatalf("re-run must report nothing, got %q", log)
	}
}

func TestEnsureProjectReportsFailedStep(t *testing.T) {
	// Fixture: setup ["false"]. The step must END with err before
	// EnsureProject returns the failure.
	var log []string
	err := wsp.EnsureProject(cfg, ws, "app", recordingStep(&log))
	if err == nil {
		t.Fatal("want setup failure")
	}
	want := []string{
		"begin checking out (branch " + ws.Alloc.TaskID + ")",
		"end checking out (branch " + ws.Alloc.TaskID + ") ok",
		"begin setup: false",
		"end setup: false err",
	}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("failure log:\n got %q\nwant %q", log, want)
	}
}

func TestEnsureProjectNilStepIsSilent(t *testing.T) {
	// nil Step must be accepted on every path — the boundary pin for every
	// caller that doesn't report (and for Task 2's not-yet-wired callers).
	if err := wsp.EnsureProject(cfg, ws, "app", nil); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/wsp/ -run TestEnsureProject -v`
Expected: FAIL to compile (EnsureProject takes 3 args; `wsp.Step` undefined).

- [ ] **Step 3: Implement**

In `internal/wsp/ensure.go`:

```go
// Step is the ensure chain's progress reporter: called with a human label
// right before a slow operation runs, it returns the done to call when the
// operation ends (err reports how). A nil Step is silence — every caller
// that has nothing to say passes nil, and EnsureProject never notices.
// Labels name only operations that ACTUALLY run: an already-checked-out
// project or a current setup stamp reports nothing, so idempotent re-runs
// stay as quiet as they always were (spec 2026-08-12 rows 1-2, Mechanics).
type Step func(label string) (done func(err error))

// begin starts a reported step, returning a done that is safe to call on
// every path. It is the nil-Step adapter: with no reporter both halves are
// no-ops.
func begin(step Step, label string) func(error) {
	if step == nil {
		return func(error) {}
	}
	return step(label)
}
```

Signature: `func EnsureProject(cfg *config.Config, ws Workspace, project string, step Step) error`.

Around the worktree creation (inside the existing `if !gitx.IsWorkTreeRoot(dest)` gate):

```go
	if !gitx.IsWorkTreeRoot(dest) {
		done := begin(step, fmt.Sprintf("checking out (branch %s)", ws.Alloc.TaskID))
		if err := gitx.WorktreeAdd(p.Repo, dest, ws.Alloc.TaskID, p.BaseBranch); err != nil {
			done(err)
			return fail(err)
		}
		done(nil)
	}
```

Around each setup command (inside the existing loop, after the stamp gate):

```go
	for _, cmd := range p.Setup {
		sub := Subst(cmd, vars)
		done := begin(step, "setup: "+sub)
		if err := proc.Run(dest, sub, env); err != nil {
			done(err)
			return fail(err) // proc.Run's message reads "command failed: <reason>"
		}
		done(nil)
	}
```

Extend EnsureProject's doc comment: one sentence on the reporter (what it
reports, that nil is silence, that skipped steps report nothing).

Update the three cli call sites to pass `nil` (compile-green, behavior
identical): `new.go:237`, `checkout.go:63`, `up.go:120`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/wsp/ -run TestEnsureProject -v`, then full
`go test ./...` (cli callers now pass nil — every existing txtar must be
byte-identical in behavior).

- [ ] **Step 5: Mutation-check the quiet-re-run pin**

Temporarily move the setup `begin` call ABOVE the `setupCurrent` gate (so a
current stamp still reports); `TestEnsureProjectReportsSteps`'s re-run half
must fail. Restore. State the check in the commit body.

- [ ] **Step 6: gofmt, vet, full suite, commit**

```bash
cd /home/cat/dev/claude-workspaces
gofmt -l . && go vet ./... && go test ./...
git add internal/wsp/ internal/cli/
git commit -m "feat(wsp): EnsureProject reports its slow steps through an optional Step reporter"
```

(Append the standard trailers.)

---

### Task 2: The cli printer, wired into new/checkout/up; txtar pins

**Files:**
- Create: `internal/cli/progress.go`
- Modify: `internal/cli/new.go` (newWork's EnsureProject call, ~line 237)
- Modify: `internal/cli/checkout.go` (checkoutWork gains the out-writer —
  change its signature to `checkoutWork(cmd *cobra.Command, cfg *config.Config, ws wsp.Workspace, projects []string) error`
  and fix its two callers: the checkout command's RunE and launch.go ~line 97)
- Modify: `internal/cli/up.go` (upProject's EnsureProject call, ~line 120)
- Modify: `internal/cli/testdata/up.txtar`, `internal/cli/testdata/launch.txtar`
  (progress pins); any other txtar that a collision surfaces (run the suite).
- Modify: `README.md` — wherever example output of `new`/`launch`/`up` is
  shown (Quick start "what just happened" narrative, Sessions example), make
  it consistent with the new lines; add one sentence to the Services or
  Quick start section: setup commands report per-command progress with
  durations.

**Interfaces:**
- Consumes: `wsp.Step`, 4-arg `wsp.EnsureProject` (Task 1).
- Produces: `projectStepper(out io.Writer, project string) wsp.Step` — Task 3
  does not consume it, but the reviewers will read it by this name.

- [ ] **Step 1: Extend the txtars to pin the NEW output (failing first)**

`up.txtar` (read it first; its fixture projects have `setup:` lines): where
a project is ensured FROM SCRATCH by `up`, pin the progress lines in order,
durations as regex, e.g.:

```
stdout '  app: checking out \(branch T-1\)… ok \(\d+\.\d+s\)'
stdout '  app: setup: echo setup-ran… ok \(\d+\.\d+s\)'
```

(match the fixture's real setup command; if the fixture setup carries a `${}`
token, the pin shows the SUBSTITUTED text). Where the txtar re-runs `up` on
an already-current project, pin the absence:

```
! stdout 'checking out \(branch T-1\)… ok'
```

(scope the negative to the re-run block — testscript `stdout`/`! stdout`
match the LAST command's output, so place the assert right after the re-run).
Where a txtar exercises a FAILING setup (`bad`'s `setup: ["false"]` in
launch.txtar's config, or up.txtar's equivalent), pin the completed failure
line:

```
stdout 'setup: false… failed'
```

`launch.txtar`: on the fresh-create block, pin one checking-out line and one
setup line if its fixture has setup commands (its `app` has none today — if
so, add a cheap one to the fixture config, e.g. `setup: ["true"]`, and keep
`exists .workspace/setup-app.ok` pins consistent — check what the file pins
about setup stamps and adjust the fixture choice accordingly).

Run: `go test ./internal/cli/ -run 'TestScripts/up' -v` (and launch)
Expected: FAIL — no progress lines are printed yet.

- [ ] **Step 2: Implement the printer**

`internal/cli/progress.go`:

```go
package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/Phaengris/claude-workspaces/internal/wsp"
)

// projectStepper renders the ensure chain's progress for one project (spec
// 2026-08-12 rows 2-4): the label line is printed WITHOUT a newline before
// the step runs, and the SAME reporter completes it — `ok (2.1s)` or
// `failed` — so piped output always ends up line-clean without any tty
// probing. Durations are %.1f seconds, always shown: `(0.0s)` is the honest
// answer for an instant step. Setup output itself stays captured in
// proc.Run; these lines NAME the commands, they do not stream them.
func projectStepper(out io.Writer, project string) wsp.Step {
	return func(label string) func(error) {
		fmt.Fprintf(out, "  %s: %s… ", project, label)
		start := time.Now()
		return func(err error) {
			if err != nil {
				fmt.Fprintln(out, "failed")
				return
			}
			fmt.Fprintf(out, "ok (%.1fs)\n", time.Since(start).Seconds())
		}
	}
}
```

Wire the three call sites:

- `new.go` (~237): `wsp.EnsureProject(cfg, ws, name, projectStepper(cmd.OutOrStdout(), name))`
- `checkout.go` (~63, inside the new `checkoutWork(cmd, …)`): same shape with
  its loop variable; update the checkout RunE and launch.go to pass `cmd`.
- `up.go` (~120, in upProject, which already has cmd): same shape with
  `w.Project`.

- [ ] **Step 3: Tests pass — full suite, collision sweep**

Run: `go test ./...`
Seven txtars run setup (new, up, launch, destroy, checkout, status_logs,
status_env): progress lines now appear in their output. Any failure is
either a too-broad pre-existing negative (`! stdout` matching a new line) or
an exact-output pin — fix the TEST only if its intent is untouched by the
new lines; if a pre-existing pin's INTENT breaks, the implementation is
wrong (spec row 6). Report which txtars needed touches and why.

- [ ] **Step 4: Mutation-check the completion pin**

Temporarily make `projectStepper`'s done a no-op (never completes the line);
the `ok \(\d+\.\d+s\)` pins must fail. Restore. State the check in the
commit body.

- [ ] **Step 5: README (same commit)**

Per the Files list: bring any shown `new`/`launch`/`up` output in line, one
sentence on per-command setup progress. Read the Quick start narrative
("What just happened") and adjust only if it now contradicts the screen.

- [ ] **Step 6: gofmt, vet, full suite, commit**

```bash
gofmt -l . && go vet ./... && go test ./...
git add internal/cli/ README.md
git commit -m "feat(cli): live per-step progress for checkout and setup, with durations"
```

---

### Task 3: Hint split — `new` keeps it, `launch` tips the parallel terminal

**Files:**
- Modify: `internal/cli/new.go` (newWork ~line 263: the hint print MOVES out;
  the `new` RunE ~line 99 prints it after newWork succeeds)
- Modify: `internal/cli/launch.go` (create path, after
  noteProjectInDescriptionSlot ~line 114: print the tip; reuse path prints
  nothing new)
- Modify: `internal/cli/testdata/new.txtar` (pin the hint), 
  `internal/cli/testdata/launch.txtar` (pin the tip on create, its absence
  on reuse)
- Modify: `README.md` — anywhere launch's or new's example output shows the
  hint (grep `workspace cd` in README); the v1-divergences appendix does NOT
  need a row (this is a v1.2→v1.3 self-change, not a v1 divergence — but the
  Sessions section's launch narrative should mention the tip if it shows
  output).

**Interfaces:**
- Consumes: nothing from Tasks 1-2 (independent, but sequenced last so txtar
  churn lands once).

- [ ] **Step 1: Extend txtars (failing first)**

`new.txtar`: after a successful create, pin (unquoted-vs-quoted per the
`$WORK` rule — this line has no variables, plain quoting):

```
stdout 'hint: workspace cd T-1'
```

`launch.txtar`: on the fresh-create block pin the tip; on the reuse block
pin both absences:

```
# create path
stdout 'tip: in another terminal: workspace cd T-1 — work alongside this session'
! stdout 'hint: workspace cd'
```
```
# reuse path (place right after the reuse exec)
! stdout 'tip: in another terminal'
! stdout 'hint: workspace cd'
```

Run: `go test ./internal/cli/ -run 'TestScripts/(new|launch)' -v`
Expected: FAIL — launch currently prints the hint via newWork, the tip
nowhere.

- [ ] **Step 2: Implement**

- `new.go`: delete the `fmt.Fprintf(out, "hint: workspace cd %s\n", taskID)`
  line from newWork (~263) and its now-stale doc-comment mention; in the
  `new` command's RunE, after the successful `newWork` call (~line 99):

```go
	fmt.Fprintf(cmd.OutOrStdout(), "hint: workspace cd %s\n", taskID)
```

- `launch.go`, create path only (inside the `errors.Is(err, xerr.ErrNotFound)`
  case, after the noteProjectInDescriptionSlot call):

```go
	// The cd hint `new` prints would read as a pending to-do here — the
	// session is about to own this terminal. What IS worth saying is how
	// to reach the workspace while the session runs (spec row 7).
	fmt.Fprintf(out, "tip: in another terminal: workspace cd %s — work alongside this session\n", taskID)
```

Update launch.go's doc comment where it describes the create-path output.

- [ ] **Step 3: Tests pass**

Run: `go test ./...`
Expected: PASS (check no other txtar pinned the launch-path hint — the
pre-plan grep found no `hint: workspace cd` pins anywhere, so failures here
mean new breakage, not stale pins).

- [ ] **Step 4: Mutation-check the split**

Temporarily restore the hint print inside newWork; launch.txtar's
`! stdout 'hint: workspace cd'` must fail while new.txtar still passes.
Restore. State the check in the commit body.

- [ ] **Step 5: README (same commit), gofmt, vet, commit**

```bash
gofmt -l . && go vet ./... && go test ./...
git add internal/cli/ README.md
git commit -m "feat(cli): launch tips the parallel terminal; the cd hint stays new's"
```

---

### Task 4: Release v1.3.0 (controller, after final review)

```bash
git checkout master && git merge --no-ff feat/ensure-progress -m "Merge feat/ensure-progress: live ensure-chain progress + launch tip (v1.3.0)"
CGO_ENABLED=0 go build -ldflags "-X github.com/Phaengris/claude-workspaces/internal/cli.version=1.3.0" -o ./workspace ./cmd/workspace
./workspace --version   # prints: workspace version 1.3.0
./workspace install
git tag v1.3.0 && git push origin master v1.3.0
git branch -d feat/ensure-progress
```

(Trailers on the merge commit; also bump the README Install build example's
`-ldflags` version to 1.3.0 in whichever earlier task touches README last —
Task 3.)

---

## Self-review notes (already applied)

- Spec rows: 1 → T1 (chain) + T2 (all three commands); 2-4 → T1 labels +
  T2 printer/pins; 5 → T1 (Step in wsp, formatting in cli); 6 → T2 step 3's
  collision sweep discipline; 7 → T3; 8 → T4 + T3's README version bump.
- Type consistency: `wsp.Step`, `begin(step, label)`, 4-arg `EnsureProject`,
  `projectStepper(out, project)`, `checkoutWork(cmd, cfg, ws, projects)` —
  names used identically across tasks.
- The output-order fact (progress before the `created` line) is a Global
  Constraint so both txtar-writing tasks place pins correctly.
