# Lazy Daemons + Service Descriptions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `launch` stops auto-starting daemons; daemons gain an optional
`description:` so sessions know what exists and what to start on demand.

**Architecture:** A third accepted `start:` entry shape in config carries the
description; `wsp.Daemon` transports it; `status`, WORKSPACE.md and doctor
render it (status/WORKSPACE.md with runtime `${}` substitution via
`wsp.Subst`/`wsp.RuntimeVars`). `launch` loses its up phase. No new stored
state anywhere — everything stays derived.

**Tech Stack:** Go 1.26, cobra, goccy/go-yaml (strict), testscript txtar.

**Spec:** `docs/superpowers/specs/2026-08-11-lazy-daemons-design.md` — its
Decided-behaviors table binds every task here.

## Global Constraints

- Clean-room: never consult or port Ruby v1 code. Spec + this repo only.
- Conventional commits; every commit ends with the two trailer lines
  (`Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>` and the
  `Claude-Session:` line) used by every commit on this branch — copy them
  from `git log -1 --format=%B`.
- `gofmt -l .` and `go vet ./...` clean before every commit.
- TDD: failing test first, then code. Mutation-check load-bearing pins and
  say so in the commit message.
- README changes in the SAME commit as the behavior it documents.
- txtar gotcha: `$VAR` expands in UNQUOTED chunks only; a real `claude` must
  never be invocable (PATH shim first).
- goccy sharp edge: unquoted `${…}` inside flow-style YAML is rejected —
  block style in all examples and fixtures.
- Work on a feature branch `feat/lazy-daemons` off master; do not push until
  the final task.

---

### Task 1: Config grammar — nested start-entry form with `description:`

**Files:**
- Modify: `internal/config/types.go` (StartEntry struct + UnmarshalYAML)
- Modify: `internal/config/decode_test.go` (new table cases)
- Modify: `assets/config_stub.yml` (show the nested form, ~line 106)
- Modify: `README.md` Configuration section (~lines 227, 241–242 show
  `start:` examples; add the nested form beside them)

**Interfaces:**
- Produces: `config.StartEntry{Name, Cmd, Description string}` — Description
  is `""` for bare strings and `{name: cmd}` entries. Task 2 consumes it.

- [ ] **Step 1: Write the failing tests**

Add to the decode tests in `internal/config/decode_test.go` (match the
file's existing table style — adapt names/harness to what is there; these
are the behaviors to pin):

```go
func TestStartEntryNestedForm(t *testing.T) {
	// Successful shapes: bare string, {name: cmd}, {name: {command,
	// description}}, and nested form WITHOUT description.
	yml := `
projects:
  app:
    repo: /tmp/x
    start:
      - bundle install
      - worker: bin/sidekiq
      - rails:
          command: bin/rails s -p ${PORT0}
          description: app server — UI at http://localhost:${PORT0}
      - quiet:
          command: sleep 30
`
	cfg := decodeString(t, yml) // use the file's existing decode helper
	got := cfg.Projects["app"].Start
	want := []config.StartEntry{
		{Cmd: "bundle install"},
		{Name: "worker", Cmd: "bin/sidekiq"},
		{Name: "rails", Cmd: "bin/rails s -p ${PORT0}",
			Description: "app server — UI at http://localhost:${PORT0}"},
		{Name: "quiet", Cmd: "sleep 30"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestStartEntryNestedFormErrors(t *testing.T) {
	cases := []struct{ name, yml, wantErr string }{
		{"missing command",
			"projects:\n  app:\n    repo: /tmp/x\n    start:\n      - rails:\n          description: d\n",
			"command"},
		{"unknown key",
			"projects:\n  app:\n    repo: /tmp/x\n    start:\n      - rails:\n          command: c\n          descriptoin: d\n",
			"descriptoin"},
		{"two names",
			"projects:\n  app:\n    repo: /tmp/x\n    start:\n      - rails: a\n        vite: b\n",
			"single"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := decodeStringErr(t, tc.yml) // or however the file asserts decode errors
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run TestStartEntry -v`
Expected: FAIL — nested-form case errors ("unexpected key" or type
mismatch), because UnmarshalYAML only accepts string and map[string]string.

- [ ] **Step 3: Implement**

In `internal/config/types.go`, extend StartEntry and its UnmarshalYAML:

```go
// StartEntry is one `start:` item: a bare string is a run-and-wait command
// (Name == ""), a single-key map is a named daemon (spec §4). The daemon
// form's value is either the command string or a {command, description}
// block — the description is what status/WORKSPACE.md show a session so it
// knows what the daemon is for before starting it.
type StartEntry struct {
	Name        string
	Cmd         string
	Description string
}

// startEntryBody is the nested daemon form. Strict decode rejects unknown
// keys like every other config struct.
type startEntryBody struct {
	Command     string `yaml:"command"`
	Description string `yaml:"description"`
}

// UnmarshalYAML accepts `cmd`, `{name: cmd}` or `{name: {command,
// description}}`. A map with any count other than one key is ambiguous and
// rejected; a nested form without `command` has no daemon to run and is
// rejected too.
func (s *StartEntry) UnmarshalYAML(unmarshal func(any) error) error {
	var str string
	if err := unmarshal(&str); err == nil {
		s.Cmd = str
		return nil
	}
	var m map[string]string
	if err := unmarshal(&m); err == nil {
		if len(m) != 1 {
			return fmt.Errorf("start entry must be a string or a single {name: command} pair, got %d keys", len(m))
		}
		for name, cmd := range m {
			s.Name, s.Cmd = name, cmd
		}
		return nil
	}
	var nested map[string]startEntryBody
	if err := unmarshal(&nested); err != nil {
		return err
	}
	if len(nested) != 1 {
		return fmt.Errorf("start entry must be a string or a single {name: command} pair, got %d keys", len(nested))
	}
	for name, body := range nested {
		if body.Command == "" {
			return fmt.Errorf("start entry %q: nested form requires command:", name)
		}
		s.Name, s.Cmd, s.Description = name, body.Command, body.Description
	}
	return nil
}
```

CHECK before assuming: how strictness reaches nested structs here — look at
`internal/config/decode.go` for the decoder options. If the unmarshal
callback does not inherit unknown-key rejection for `startEntryBody`, decode
into `map[string]map[string]string` instead and reject keys other than
`command`/`description` by hand (that also makes the "unknown key" test's
error message yours to phrase — keep the misspelled key name in it).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/config/ -v`
Expected: PASS (all — existing shapes must still decode).

- [ ] **Step 5: Mutation-check the load-bearing pin**

Temporarily make missing `command` NOT an error (skip the `body.Command ==
""` check); `TestStartEntryNestedFormErrors/missing command` must fail.
Restore.

- [ ] **Step 6: Update the two docs surfaces (same commit)**

`assets/config_stub.yml` — extend the `start:` block (~line 104) so the stub
teaches all three shapes; keep block style:

```yaml
#     start:                    # what `up` runs, in order
#       - bin/rails db:migrate  #   a bare string: run it and wait
#       - rails:                #   a daemon with a description — what
#           command: bin/rails s -p ${PORT0}   # status/WORKSPACE.md tell a
#           description: app server — UI at http://localhost:${PORT0}   # session it's for
#       - worker: bin/sidekiq   #   {name: command}: a daemon, description-less
```

`README.md` Configuration reference (~lines 241–242): add the nested form
next to the existing `{name: cmd}` daemon example, with one sentence: the
optional `description:` is shown by `status`/WORKSPACE.md (with `${}`
substituted) so a session knows what each daemon is for.

- [ ] **Step 7: gofmt, vet, full test, commit**

```bash
cd /home/cat/dev/claude-workspaces
gofmt -l . && go vet ./... && go test ./...
git add internal/config/ assets/config_stub.yml README.md
git commit -m "feat(config): start entries accept {name: {command, description}}"
```

(Append the standard trailers; note the mutation check in the body.)

---

### Task 2: Carry description through wsp; render it in `status`

**Files:**
- Modify: `internal/wsp/daemon.go` (Daemon struct + DaemonsOf, ~lines 28–59)
- Modify: `internal/wsp/daemon_test.go` (DaemonsOf carries Description)
- Modify: `internal/cli/status.go` (statusDaemon struct ~line 48,
  statusDaemonsOf ~line 174, statusDaemonDetail ~line 256)
- Modify: `internal/cli/testdata/status_env.txtar` (pin the rendered line)
- Modify: `README.md` Services & daemons section (~line 482) if it shows
  status daemon lines — make any shown output match the new rendering.

**Interfaces:**
- Consumes: `config.StartEntry.Description` (Task 1).
- Produces: `wsp.Daemon{Project, Name, Cmd, Description string}` — Tasks 3
  and 5 consume Description. Status JSON gains
  `"description,omitempty"`.

- [ ] **Step 1: Write the failing wsp test**

In `internal/wsp/daemon_test.go`, extend (or add beside) the DaemonsOf test:

```go
func TestDaemonsOfCarriesDescription(t *testing.T) {
	cfg := &config.Config{Projects: map[string]*config.Project{
		"app": {Start: []config.StartEntry{
			{Cmd: "bundle install"},
			{Name: "rails", Cmd: "bin/rails s", Description: "app server"},
			{Name: "worker", Cmd: "bin/sidekiq"},
		}},
	}}
	got := wsp.DaemonsOf(cfg, "app")
	want := []wsp.Daemon{
		{Project: "app", Name: "rails", Cmd: "bin/rails s", Description: "app server"},
		{Project: "app", Name: "worker", Cmd: "bin/sidekiq"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}
```

(Match the file's actual package/import/helper style.)

- [ ] **Step 2: Run it — expect compile failure** (`unknown field Description`).

Run: `go test ./internal/wsp/ -run TestDaemonsOfCarriesDescription`

- [ ] **Step 3: Implement the carry-through**

`internal/wsp/daemon.go`: add `Description string` to `Daemon` (extend the
struct comment: the config description, verbatim — renderers substitute
`${}` themselves), and in `DaemonsOf` copy it:

```go
out = append(out, Daemon{Project: project, Name: e.Name, Cmd: e.Cmd, Description: e.Description})
```

- [ ] **Step 4: wsp tests pass**

Run: `go test ./internal/wsp/`
Expected: PASS.

- [ ] **Step 5: Extend the status txtar (failing first)**

In `internal/cli/testdata/status_env.txtar`: give one configured daemon in
the fixture config a description containing a `${}` value token (use a
value the fixture already computes, e.g. `PORT0`), in block style:

```yaml
    start:
      - sleeper:
          command: sleep 30
          description: naps on port ${PORT0}
```

and pin, near the existing daemon-line asserts:

- human: a stopped line `'    sleeper: stopped — naps on port 18300'`
  (compute the real number: value start + index*per_workspace + n for the
  fixture's actual values) and, where the txtar already asserts a running
  daemon line, the running form `'sleeper: running \(pid \d+\) — naps on port 18300'`.
- a daemon WITHOUT description keeps its exact old line (add/keep one such
  daemon and pin `'    other: stopped$'` — regexp-anchor so a stray suffix
  fails).
- `--json`: `"description": "naps on port 18300"` present for the described
  daemon; for the description-less daemon the key is ABSENT (`omitempty`) —
  pin with `! stdout '"description": ""'` plus the positive match.

Run: `go test ./internal/cli/ -run 'TestScripts/status_env' -v`
Expected: FAIL on the new asserts.

- [ ] **Step 6: Implement status rendering**

`internal/cli/status.go`:

```go
type statusDaemon struct {
	Name        string `json:"name"`
	Running     bool   `json:"running"`
	Pid         int    `json:"pid"`
	Description string `json:"description,omitempty"`
}
```

`statusDaemonsOf` substitutes once per project (add imports as needed):

```go
func statusDaemonsOf(cfg *config.Config, ws wsp.Workspace, project string) []statusDaemon {
	ds := wsp.DaemonsOf(cfg, project)
	vars := wsp.RuntimeVars(cfg, ws.Alloc.TaskID, project, ws.Alloc.Index)
	out := make([]statusDaemon, 0, len(ds))
	for _, d := range ds {
		running, pid := wsp.DaemonState(ws, d)
		out = append(out, statusDaemon{
			Name:        d.Name,
			Running:     running,
			Pid:         pid,
			Description: wsp.Subst(d.Description, vars),
		})
	}
	return out
}
```

`statusDaemonDetail` appends the description with the file's existing
`taskSep` (" — ", already a const at line 21):

```go
func statusDaemonDetail(d statusDaemon) string {
	s := "stopped"
	if d.Running {
		s = fmt.Sprintf("running (pid %d)", d.Pid)
	}
	if d.Description != "" {
		s += taskSep + d.Description
	}
	return s
}
```

Keep/extend the function's doc comment: description absent → line
byte-identical to before (no trailing separator).

- [ ] **Step 7: Full cli tests pass**

Run: `go test ./internal/cli/`
Expected: PASS.

- [ ] **Step 8: gofmt, vet, commit**

```bash
gofmt -l . && go vet ./... && go test ./...
git add internal/wsp/ internal/cli/ README.md
git commit -m "feat(status): daemon descriptions, substituted, in human and JSON output"
```

---

### Task 3: WORKSPACE.md services block

**Files:**
- Modify: `internal/wsp/write.go` (WriteWorkspaceMD, after the instructions
  block ~line 93)
- Modify: `internal/wsp/write_test.go` + `internal/wsp/testdata/workspace_md.golden`
- Modify: `README.md` — wherever it describes WORKSPACE.md's contents
  (search `WORKSPACE.md`), add "each project's daemons and what they're
  for" to the list.

**Interfaces:**
- Consumes: `wsp.Daemon.Description` (Task 2), `wsp.RuntimeVars`/`wsp.Subst`.

- [ ] **Step 1: Extend the golden (failing first)**

Read `internal/wsp/write_test.go` to see the fixture config feeding
`workspace_md.golden`. Give one of its daemons a description (with a `${}`
token over a fixture value) and add a description-less daemon if none
exists. Extend `testdata/workspace_md.golden` with the block below, placed
inside each project section that has daemons, AFTER the instructions text:

```
Services — not started automatically; start what you need with
`workspace up <workspace-name> <daemon>` and follow it with
`workspace logs <workspace-name> <daemon>`:

- rails — app server — UI at http://localhost:18300
- worker
```

where `<workspace-name>` is the LITERAL workspace name the golden already
uses (the same string as its `# <name>` heading — the reader copies the
command verbatim), each daemon in listed (start) order, ` — ` + substituted
description only when present. A project with no daemons gets no block.

Run: `go test ./internal/wsp/ -run TestWriteWorkspaceMD -v` (adjust to the
real test name)
Expected: FAIL — golden mismatch.

- [ ] **Step 2: Implement**

In `WriteWorkspaceMD`, after the instructions block inside the project
loop:

```go
if ds := DaemonsOf(cfg, st.Name); len(ds) > 0 {
	vars := RuntimeVars(cfg, ws.Alloc.TaskID, st.Name, ws.Alloc.Index)
	fmt.Fprintf(&b, "\nServices — not started automatically; start what you need with\n"+
		"`workspace up %s <daemon>` and follow it with\n"+
		"`workspace logs %s <daemon>`:\n\n", ws.Name(), ws.Name())
	for _, d := range ds {
		if d.Description == "" {
			fmt.Fprintf(&b, "- %s\n", d.Name)
			continue
		}
		fmt.Fprintf(&b, "- %s — %s\n", d.Name, Subst(d.Description, vars))
	}
}
```

Update WriteWorkspaceMD's doc-comment guarantees list (it enumerates what
the golden must keep) to include the services block.

- [ ] **Step 3: Tests pass**

Run: `go test ./internal/wsp/ ./internal/cli/`
Expected: PASS. If any cli txtar greps WORKSPACE.md content near daemons,
fix expectations there too (launch.txtar greps `### api` — unaffected
unless api gains daemons; leave api daemon-less).

- [ ] **Step 4: gofmt, vet, commit**

```bash
gofmt -l . && go vet ./... && go test ./...
git add internal/wsp/ README.md
git commit -m "feat(wsp): WORKSPACE.md lists each project's services and their descriptions"
```

---

### Task 4: `launch` goes lazy — remove the up phase

**Files:**
- Modify: `internal/cli/launch.go` (phase 2, lines ~119–131; doc comments
  and Short/Long text)
- Modify: `internal/cli/testdata/launch.txtar`
- Check: `internal/cli/launch_test.go` (or wherever TestLaunchExitCodes
  lives) for cases that exercise the up phase
- Modify: `README.md` — Commands table row ~line 358 (`new`-or-reuse +
  `checkout` + `up` + `claude` → drop the `up`), Sessions section ~line 627
  ("create-or-reuse, check out, bring up" → drop bring-up, state the lazy
  convention + `workspace up <ws> <daemon>`), and the v1-divergences
  appendix ~line 734: new row — v1 launch started daemons; v1.1 launch
  starts none and no longer converges dead daemons on reuse; `up` is the
  explicit start.

**Interfaces:**
- Consumes: nothing new. `hintNothingCheckedOut(cmd, ws)` and
  `wsp.ProjectStates(cfg, ws)` already exist (see launch.go/up.go).

- [ ] **Step 1: Rewrite launch.txtar to pin the NEW behavior (failing first)**

In `internal/cli/testdata/launch.txtar`:

- Fresh-launch block: replace `stdout 'started app:sleeper \(pid \d+\)'` and
  `exists …/pids/app:sleeper` with the negative pins:

```
! stdout 'started'
! exists $WORK/root/T-1_fix-thing/.workspace/pids
```

  (keep the creation/argv/cwd/WORKSPACE asserts and setup-ok exists — setup
  still runs via `new`).
- Reuse block: drop `stdout 'app:sleeper already running \(pid \d+\)'`.
- The "up-phase failure" block (flaky) documents a phase that no longer
  exists. Replace it with the opposite pin — flaky's failing run-and-wait
  must NOT run, so launch now SUCCEEDS:

```
# --- no up phase: a project whose start would fail launches fine ---------------
# flaky's run-and-wait is `false`; with launch lazy it never runs — the session
# starts, and the failure is deferred to an explicit `workspace up`.
env CLAUDE_SHIM_LOG=$WORK/claude-flaky.log
exec workspace launch T-1 'fix thing' flaky
exists $WORK/root/T-1_fix-thing/flaky/README
grep -count=1 'invoked' $WORK/claude-flaky.log
! exec workspace up T-1 flaky
stderr 'project "flaky"'
```

- Header comment: rewrite the first line ("create (or reuse) + checkout +
  up + claude session" → drop up; note daemons are not started).
- The `workspace down T-1` epilogue: nothing starts daemons in the script
  any more — replace the epilogue with a start/stop roundtrip that ALSO
  pins the lazy gap end-to-end:

```
# --- daemons start only when asked -------------------------------------------
exec workspace up T-1 app
stdout 'started app:sleeper \(pid \d+\)'
exec workspace down T-1
stdout 'stopped app:sleeper \(TERM\)'
```

Run: `go test ./internal/cli/ -run 'TestScripts/launch' -v`
Expected: FAIL (`started app:sleeper` still printed by the old phase 2).

- [ ] **Step 2: Implement**

In `internal/cli/launch.go` replace phase 2 (the whole
`wsp.ResolveTargets`/`upWork` block, ~lines 119–131) with:

```go
// --- phase 2: nothing starts -------------------------------------
// Daemons are lazy (spec 2026-08-11): the session starts what it
// needs via `workspace up`. The one thing launch still says here is
// the empty-workspace hint — a workspace with nothing checked out is
// a legitimate place to think in, but deserves the pointer.
if len(wsp.ProjectStates(cfg, ws)) == 0 {
	hintNothingCheckedOut(cmd, ws)
}
```

Update the command's doc comment (PHASES list: create-or-reuse → checkout →
session; note the reuse path no longer converges daemons) and the
`Short`/`Long` strings: drop "start daemons", add one line — "Daemons are
not started; use `workspace up` for the services you need." Drop the
`config`/`wsp` imports only if now unused (they are not — `wsp` still
resolves; check compile).

- [ ] **Step 3: Tests pass**

Run: `go test ./internal/cli/`
Expected: PASS, including exit-code tests (check TestLaunchExitCodes — if
any case provoked an up-phase failure for its exit code, that case moves to
`up`'s own tests or is deleted; `launch` can no longer exit through up).

- [ ] **Step 4: gofmt, vet, full suite, commit**

```bash
gofmt -l . && go vet ./... && go test ./...
git add internal/cli/ README.md
git commit -m "feat(launch)!: daemons are lazy — launch no longer runs the up phase"
```

(The `!`: user-visible behavior change; the commit body cites the spec's
decided rows 1 and 4.)

---

### Task 5: Doctor notes a description-less daemon

**Files:**
- Modify: `internal/cli/doctor.go` (doctorObserve ~line 178: new advisory
  pass FIRST, before allocations; a new `kind` const beside the existing
  `kind*` ones)
- Modify: `internal/cli/testdata/doctor_ok.txtar` and
  `internal/cli/testdata/doctor_findings.txtar`

**Interfaces:**
- Consumes: `wsp.DaemonsOf` + `Daemon.Description` (Task 2).

- [ ] **Step 1: Extend the doctor txtars (failing first)**

`doctor_ok.txtar`: its fixture config presumably has daemons without
descriptions — doctor's stdout asserts change. Give every fixture daemon a
description EXCEPT one, then pin:

```
stdout 'note: daemon app:undescribed has no description — add description: under start: to tell sessions what it is for'
stdout 'doctor: no findings'
```

(the note must NOT bump the summary count — `doctor: no findings` is the
load-bearing pin). In the `--json` block of whichever txtar checks JSON:
the note appears under `"informational"`, and `"findings": []` stays.

`doctor_findings.txtar`: add one description-less daemon to its config and
pin that `doctor: N finding(s)` keeps its EXISTING N (the note is
uncounted) while the note line appears.

Run: `go test ./internal/cli/ -run 'TestScripts/doctor' -v`
Expected: FAIL on the new note lines.

- [ ] **Step 2: Implement**

In `internal/cli/doctor.go`, add the kind const next to the existing ones
(match their naming style, e.g. `kindDaemonNoDescription = "daemon-no-description"`),
and at the TOP of `doctorObserve` (config-level advisories come first —
they belong to the config section the human rendering prints just before
the obs lines):

```go
// Config-level advisory, once per configured daemon (not per workspace):
// a daemon without a description: is invisible-in-purpose to sessions,
// which now must decide what to start (daemons are lazy). A note, never a
// finding — nothing is broken and no command fixes it; the fix is an edit
// to config.yml.
for _, project := range slices.Sorted(maps.Keys(cfg.Projects)) {
	for _, d := range wsp.DaemonsOf(cfg, project) {
		if d.Description == "" {
			note(kindDaemonNoDescription, "", fmt.Sprintf(
				"note: daemon %s has no description — add description: under start: to tell sessions what it is for",
				d.Key()))
		}
	}
}
```

(The `note` helper closure exists at ~line 183 — move its definition above
this loop if needed. Add `slices`/`maps` imports if absent.)

- [ ] **Step 3: Tests pass; mutation-check the uncounted pin**

Run: `go test ./internal/cli/`
Expected: PASS. Mutation check: flip `note:` emission to a finding
(`note: false`); the `doctor: no findings` pin in doctor_ok.txtar must
fail. Restore.

- [ ] **Step 4: gofmt, vet, commit**

```bash
gofmt -l . && go vet ./... && go test ./...
git add internal/cli/
git commit -m "feat(doctor): note (uncounted) for daemons without a description"
```

---

### Task 6: Skill + hook teach the convention; release v1.1.0

**Files:**
- Modify: `assets/skill/SKILL.md`
- Modify: `assets/hooks/session-start.sh`
- Check: `internal/cli/testdata/install.txtar` (or wherever install tests
  pin asset content) — update any pinned lines that changed
- Modify: `README.md` — final consistency pass (Quick start ~line 123 and
  Services section ~line 482 must not claim launch/new starts anything)

**Interfaces:** none new — this task is copy + release mechanics.

- [ ] **Step 1: SKILL.md**

In `assets/skill/SKILL.md`:

- The one-shot paragraph (~line 43): replace
  "`launch` = create-or-reuse the workspace → check the listed projects out
  → start their daemons → open a Claude Code session…" with
  "`launch` = create-or-reuse the workspace → check the listed projects out
  → open a Claude Code session in the workspace dir. **Daemons are not
  started.**"
- The steps table (~line 54): `run setup + start daemons` row stays (that IS
  `up`), but add a row or merge: `start one service | workspace up <ws> <daemon>`.
- Add a short section after "Working inside a workspace" (~line 85):

```markdown
## Services are lazy

Nothing starts a daemon until you ask. The session-start status block (and
`workspace status <ws>`) lists every configured daemon, whether it runs,
and — when the config describes it — what it is for:

    rails: stopped — app server — UI at http://localhost:20800

Start exactly what the task needs (`workspace up <ws> rails`), check it
with `workspace logs <ws> rails`, and `workspace down <ws>` when done.
WORKSPACE.md carries the same service list per project.
```

- [ ] **Step 2: Hook line**

In `assets/hooks/session-start.sh`, after the `Manage this workspace…`
printf (keep exit 0 flow intact):

```sh
printf 'Daemons are not auto-started; start what you need with: workspace up %s <daemon>\n' "$name"
```

- [ ] **Step 3: Full suite + asset pins**

Run: `go test ./...`
Fix any install/asset txtar pins that greps the changed asset lines.
Expected: PASS everywhere, `gofmt -l .` and `go vet ./...` clean.

- [ ] **Step 4: Commit the assets**

```bash
git add assets/ internal/cli/testdata/ README.md
git commit -m "feat(assets): skill and session hook teach the lazy-daemons convention"
```

- [ ] **Step 5: Merge + release (after final review — see handoff)**

On master after the branch merges (fast-forward or merge commit per repo
habit):

```bash
git checkout master && git merge --no-ff feat/lazy-daemons -m "Merge feat/lazy-daemons: lazy launch + daemon descriptions"
CGO_ENABLED=0 go build -ldflags "-X github.com/Phaengris/claude-workspaces/internal/cli.version=1.1.0" -o ./workspace ./cmd/workspace
./workspace version   # prints: workspace version 1.1.0
./workspace install   # idempotent, manifest-driven; refreshes skill + hook
git tag v1.1.0 && git push origin master v1.1.0
```

(Trailers on the merge commit too. The installed hook/skill on the real
machine are refreshed by `install` — no settings.json edit needed, the
hook path is unchanged.)

---

## Self-review notes (already applied)

- Spec §1 allows either hint mechanism; the plan picks `ProjectStates`
  (no resolve, cannot start anything) — the stricter reading.
- Spec coverage: decided rows 1,4 → Task 4; row 2 → Tasks 1–3; row 3 → no
  change (pinned by the new launch.txtar up/down roundtrip); row 5 → Tasks
  2–3; row 6 → Task 5; row 7 → grammar only allows descriptions on named
  entries by construction; row 8 → Task 6.
- Type consistency: `StartEntry.Description` (T1) → `Daemon.Description`
  (T2) → consumed by name in T3/T5; `statusDaemon.Description` JSON-tagged
  omitempty (T2) — names match across tasks.
