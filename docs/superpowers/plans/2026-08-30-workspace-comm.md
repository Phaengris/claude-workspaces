# `workspace comm` Implementation Plan

> **For agentic workers:** this plan is NOT for subagent execution. It is a
> kata (spec §9): Claude executes its tasks inline in the pairing session;
> cat hand-writes the marked tasks. For cat's tasks the plan deliberately
> contains contracts, tests and pattern pointers — never the implementation
> code. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a durable, root-global announcements board (`workspace comm`) so
parallel agents coordinate code changes across time, delivered automatically
by Claude Code hooks.

**Architecture:** new leaf package `internal/comm` (JSONL store behind a
`Store` interface, per-workspace timestamp cursors) + cobra wiring in
`internal/cli/comm.go` + hook delivery (SessionStart / UserPromptSubmit /
PreToolUse) + doctor/gc/skill/docs integration.

**Tech Stack:** Go stdlib (`encoding/json`, `time`, `regexp`), existing
`alloc.WithLock` flock, cobra, testscript txtar.

**Spec:** `docs/superpowers/specs/2026-08-30-workspace-comm-design.md` —
the Decided-behaviors table (D1–D20) governs; this plan cites it as D<n>.

## Global Constraints

- `CGO_ENABLED=0`; no new module dependencies (D2, §sqlite discussion).
- Work on branch `feat/comm`; master stays green; merge when the feature is
  whole; tag v1.8.0 after `./workspace install` on the real machine (D20).
- Constants exactly: `Window = 30 * 24 * time.Hour`, `MaxText = 4096`,
  `HeadlinesOver = 10` (D5, D8, D13). No config knobs.
- `ts` strictly increasing, RFC3339Nano; cursor stores a timestamp, never an
  id (D3, D6).
- All writes under `alloc.WithLock(root, …)` + atomic temp/fsync/rename;
  reads lock-free (D4).
- Exit codes: 1 I/O, 2 usage (empty/oversized text), 3 outside a workspace
  for `put`/`get --new`, 4 broken config — pinned exactly in Go tests.
- Hooks always exit 0 and print nothing outside a workspace / without the
  binary (D10). Doctrine amendment verbatim in the hook headers: “hooks add
  context and may record what they delivered (the comm cursor); they never
  create, start or modify a workspace.”
- gofmt + vet clean before every commit; commits conventional
  (`feat(comm): …`); mutation-checked pins stated in commit messages.

## File structure

| File | Responsibility | Author |
|---|---|---|
| `internal/comm/comm.go` | `Record`, `Store`, constants, `Open`, filter helpers’ signatures | Claude |
| `internal/comm/jsonl.go` | the JSONL `Store` implementation | **cat** |
| `internal/comm/cursor.go` | `ReadCursor` / `WriteCursor` | **cat** |
| `internal/comm/filter.go` | `Unread` / `Since` / `Grep` / `Headline` | **cat** |
| `internal/comm/follow.go` | `Follow` | **cat** (gear 2) |
| `internal/comm/comm_test.go`, `follow_test.go` | unit tests | Claude |
| `internal/cli/comm.go` (+`comm_test.go`, `testdata/comm.txtar`) | cobra wiring, formatting, `--hook` | Claude |
| `assets/hooks/comm-deliver.sh`, `session-start.sh` | delivery | Claude |
| `internal/assets/assets.go`, `internal/cli/install.go` | embed + manifest + snippets | Claude |
| `internal/cli/doctor.go`, `gc.go` | two kinds; prune pass | Claude |
| `assets/skill/SKILL.md`, README, `docs/reference.md`, CHANGELOG | culture + contracts | Claude |

---

### Task 1: `internal/comm` skeleton + unit tests (Claude)

**Files:** create `internal/comm/{comm.go,jsonl.go,cursor.go,filter.go}`,
`internal/comm/comm_test.go`.

**Interfaces (produced — later tasks and cat’s bodies rely on these exactly):**

```go
type Record struct {
    TS   time.Time `json:"ts"`   // RFC3339Nano, strictly increasing per board
    From string    `json:"from"` // workspace name
    Text string    `json:"text"` // freeform; first line = headline
}
const Window = 30 * 24 * time.Hour
const MaxText = 4096
const HeadlinesOver = 10

type Store interface {
    Append(from, text string, now time.Time) (Record, error)
    List(warn func(string)) ([]Record, error)
    Prune(now time.Time) error
}
func Open(root string) Store // JSONL store at <root>/.comm.jsonl

func ReadCursor(wsDir string) time.Time // zero when missing/unparsable
func WriteCursor(wsDir string, ts time.Time) error // <wsDir>/.workspace/comm-cursor, temp+rename

func Unread(recs []Record, cursor time.Time, self string, now time.Time) []Record
func Since(recs []Record, cutoff time.Time) []Record
func Grep(recs []Record, re *regexp.Regexp) []Record
func Headline(r Record) string
```

- [ ] Write the four source files: full doc comments stating each contract
  (from spec Mechanics + D2–D6, D11); every body is
  `panic("comm: not implemented — this one is cat's")`.
- [ ] Write `comm_test.go` — complete, runnable, table-driven. Cases (each
  with exact expected values):
  - Append: assigns `now` when now > last; **same-instant and
    clock-backwards → last+1ns (mutation-checked pins)**; persists and
    List round-trips; prunes a record older than `Window` in the same
    write; empty file → first record stamped `now`.
  - Append concurrency (`-race`): 20 goroutines × 5 records → 100 records,
    strictly increasing TS, no duplicates.
  - List: skips a malformed middle line, `warn` called once with the line
    number, both neighbors returned; missing file → empty, no warn.
  - Prune: drops only records `< now-Window`; file rewritten atomically
    (no temp litter).
  - Cursor: round trip; missing file → zero time; garbage content → zero;
    WriteCursor creates `.workspace/` if absent.
  - Unread: excludes `ts ≤ cursor`, excludes `from == self`, excludes
    older-than-window, keeps order.
  - Since / Grep / Headline: boundary cases (cutoff exactly equal — excluded;
    regexp matches From as well as Text; single-line text → itself).
- [ ] `go test ./internal/comm` — every test FAILS by panic (skeleton).
- [ ] `gofmt -l . && go vet ./...` clean; commit
  `feat(comm): package skeleton and unit tests (red — kata)` on `feat/comm`.

### Task 2: bodies of the store, cursor and filters (**cat**)

**Consumes:** Task 1’s signatures and tests — the tests are the definition
of done. **Produces:** a green `go test ./internal/comm` (minus Follow).

The code is deliberately not in this plan. Guidance:

- [ ] Pattern to read first: `internal/alloc/registry.go` (Load / atomic
  Save / WithLock) — `jsonl.go` is its sibling: load lines with
  `bufio.Scanner` + `json.Unmarshal` per line, save via temp file → fsync →
  rename, whole write under `alloc.WithLock(root, …)`.
- [ ] Suggested order: `cursor.go` (smallest) → `filter.go` (pure
  functions) → `jsonl.go` (Load/Save → Append → Prune).
- [ ] The TS rule lives in ONE place (Append): `if !now.After(last) {
  now = last.Add(1) }` is the whole idea — the tests pin both branches.
- [ ] Run `go test ./internal/comm -run <Case> -v` per case; finish with
  the full package incl. `-race`.
- [ ] Commit as author (Claude reviews the diff like any implementer’s).

### Task 3: `Follow` (**cat**, gear 2 — description-first)

- [ ] Claude writes `follow_test.go` (start Follow with 5ms interval against
  a store; append two records while it runs; cancel the context; assert both
  were written to the buffer exactly once, in order, and Follow returned
  `ctx.Err()`) plus the doc-comment contract: re-List every interval, print
  records with TS newer than the last printed via the same rendering
  callback the CLI passes (signature per spec:
  `Follow(ctx context.Context, s Store, w io.Writer, interval time.Duration) error`
  — rendering fixed inside as `<YYYY-MM-DD HH:MM>  <from>\n  <text-indented>\n\n`).
- [ ] cat designs and writes `follow.go` — loop shape, state, shutdown are
  cat’s decisions; the test is the contract.
- [ ] Green incl. `-race`; commit.

### Task 4: CLI wiring (Claude)

**Files:** create `internal/cli/comm.go`, `internal/cli/comm_test.go`,
`internal/cli/testdata/comm.txtar`; modify `internal/cli/root.go` (register
`newCommCmd()`), `internal/cli/completion.go` (`"comm": completeNothing`).

**Consumes:** every Task 1 symbol. **Produces:** the D-table command surface:

- [ ] `newCommCmd()` with subcommands `put`, `get`, `listen`, `prune`
  (parents: `loadRootDir()` for the root; `workspaceAt(reg, cwd)` where
  required — exit 3 via `xerr.Wrap(xerr.ErrNotFound, …)` as `which` does).
  - `put`: args joined by space, or stdin when no args / `-`; trim; empty →
    exit 2; `len(text) > comm.MaxText` → exit 2 with the D13 message;
    `Append`; print `posted <RFC3339 ts>`.
  - `get`: default window view (`Since(recs, now-Window)`); `--new`
    (Unread + advance cursor after successful print; >HeadlinesOver →
    headlines + hint); `--since <dur>` (custom parser: Go durations plus
    `d`=24h, `w`=7d suffixes); `--grep <re>` (compiled
    `(?i)` regexp, combinable with `--since`); `--json` (records array);
    `--hook <event>`: with pending records, print
    `{"hookSpecificOutput":{"hookEventName":"<event>","additionalContext":"…"}}`
    (JSON-marshalled by Go — no shell escaping anywhere), advance cursor;
    silent success when empty.
  - `listen`: print last 20 window records, then `comm.Follow` with 1s
    interval and a SIGINT-cancelled context.
  - `prune`: `Prune(now)`, print nothing.
- [ ] txtar flow (wsenv/workroot conventions from `cli_test.go`): put
  inside → `posted`; put outside → error text (code pinned in Go test);
  `get --new` shows the sibling’s post, not its own; second `--new` prints
  nothing (cursor); 11 posts → headlines + hint; `--since 1h` / `--grep` /
  `--json`; oversized put refused; corrupt line → one stderr warning, get
  still lists the good records.
- [ ] Go tests pin exit codes 2/3 exactly and `--hook` output is valid JSON
  (round-trip `json.Unmarshal`).
- [ ] Full suite + race pass; commit `feat(cli): workspace comm command`.

### Task 5: hooks + install (Claude)

**Files:** create `assets/hooks/comm-deliver.sh`; modify
`assets/hooks/session-start.sh`, `internal/assets/assets.go` (embed +
accessor `CommHook()` naming target
`~/.local/share/workspace/hooks/comm-deliver.sh`, 0755),
`internal/cli/install.go` (layout field, write, manifest, snippet),
install/uninstall tests, plus the assets round-trip test.

- [ ] `comm-deliver.sh` (complete):

```sh
#!/bin/sh
# Delivery hook for the workspace comm board (UserPromptSubmit, PreToolUse).
# Doctrine: hooks add context and may record what they delivered (the comm
# cursor); they never create, start or modify a workspace. Exit 0 always.
command -v workspace >/dev/null 2>&1 || exit 0
event=$(sed -n 's/.*"hook_event_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' 2>/dev/null)
[ -n "$event" ] || event=PreToolUse
workspace comm get --new --hook "$event" 2>/dev/null
exit 0
```

- [ ] `session-start.sh`: after the status block, capture
  `workspace comm get --new 2>/dev/null`; if non-empty, print
  `## Announcements from other workspaces` + the output (plain stdout —
  SessionStart context needs no JSON).
- [ ] Implementation check (D9): confirm current Claude Code docs list
  `additionalContext` for PreToolUse `hookSpecificOutput`; if absent, wire
  the snippet to PostToolUse instead and note it in CHANGELOG.
- [ ] `install` prints the two hook snippets beside the SessionStart one;
  uninstall removes by manifest (tests: manifest contains the new path;
  survivors logic untouched).
- [ ] Suite green; commit `feat(install): comm delivery hooks`.

### Task 6: doctor + gc (Claude)

**Files:** modify `internal/cli/doctor.go` (+`doctor_findings.txtar` or new
txtar), `internal/cli/gc.go` (+txtar).

- [ ] Doctor kinds `kindCommCorruptLine = "comm_corrupt_line"` (finding,
  message names the line number) and `kindCommCursorUnparsable =
  "comm_cursor_unparsable"` (note, names the workspace) — root-level pass
  using `Store.List` warn callback and `ReadCursor` on each workspace.
- [ ] gc: after the stale-pid pass, `comm.Open(root).Prune(now)`; one line
  `pruned announcements older than 30d` only when records were dropped
  (List before/after under the same command; silence otherwise).
- [ ] txtar pins for both; suite green; commit.

### Task 7: skill + docs (Claude)

**Files:** modify `assets/skill/SKILL.md`, `README.md`,
`docs/reference.md`, `CHANGELOG.md`.

- [ ] SKILL.md: new `## The announcements board` per D18 — absorbs
  “Coordinating with sibling sessions” as the live half; the four templates
  (started / paused / finished with merge target+time / joint decision);
  headline-first; concrete file/class names; facts not instructions;
  discussion private → outcome announced once by the agreed signer; judge
  relevance yourself; sleeping-workspace procedure (`status <ws>`, branch,
  `ListAgents`, ask the user to wake it); announcements arrive via hooks —
  don’t poll, but `comm get --grep <area>` before touching a shared area.
- [ ] README: short “Coordination” paragraph (the two channels, one
  example post) linking to reference; reference.md: the full contract
  (command table, window/cursor semantics, hook events, doctrine
  amendment) — same-commit accuracy rule.
- [ ] CHANGELOG under Unreleased: Added — the board, the hooks, the skill
  section; note the install snippets for existing installs.
- [ ] Commit `docs: comm board — skill, README, reference, changelog`.

### Task 8: land + release (Claude, with cat)

- [ ] Independent review of the whole branch diff (blast-radius rule; a
  capable reviewer subagent), findings fixed.
- [ ] Merge `feat/comm` → master; push BOTH remotes; CI green.
- [ ] Build with git-describe version; `./workspace install`; add the two
  hook snippets to `~/.claude/settings.json` (user does — tool never edits).
- [ ] Prove it live: two real workspaces, post from one, watch delivery in
  the other’s session and in `comm listen`.
- [ ] When cat calls it good in daily use: retitle Unreleased → 1.8.0,
  build with `version=1.8.0`, install, tag `v1.8.0`, push master+tag to
  both remotes.

## Self-review

- Spec coverage: D1–D20 → T1 (D2–D6, D11), T4 (D7–D8, D12–D14, D16),
  T5 (D9–D10, D19), T6 (D14–D15), T7 (D18, D20 docs), T8 (D20 release);
  §9 split honored (T2/T3 cat). Out-of-scope list untouched by any task. ✓
- No placeholders in Claude tasks; cat tasks intentionally carry contracts,
  not code — stated in the header. ✓
- Type consistency: signatures identical in T1 interfaces, T3 contract and
  T4 consumption; constants named once. ✓
