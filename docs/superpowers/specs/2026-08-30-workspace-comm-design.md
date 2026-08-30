# `workspace comm` — the announcements board — design

**Date:** 2026-08-30
**Status:** approved (brainstormed with cat over 2026-08-27 … 08-30; Fizzy card #119)
**Ships as:** v1.8.0
**Build format:** kata — Claude writes the spec, plan, package skeleton and
failing tests; cat writes the bodies (see §9).

## Motivation

Workspaces isolate ports, databases, env and checkouts. They cannot isolate
the one shared resource that has no copies: the code itself. Two agents in
two worktrees of the same repo edit the same logical codebase, and git only
reports *textual* collisions, at merge time — far too late for "I deleted
the interface you started building on." Observed in daily use: one agent
relied on a feature another was mid-rewrite on.

Ports get separated by allocation; code cannot be separated, so it gets
**coordinated**. Two channels, two jobs:

- **Live conversation** — Claude Code's native cross-session messaging
  (`ListAgents`/`SendMessage`, v2.1.224+): point-to-point, immediate,
  reaches only sessions running right now. Taught by the skill since
  2026-08-27 ("phase 0", card #118). Nothing to build.
- **Intent across time** — this feature. An agent that reworked an
  interface yesterday and exited cannot answer today's question; a workspace
  mid-flight whose agent is asleep cannot warn anyone. A durable,
  root-global, broadcast **announcements board** carries that: "started work
  on X", "paused, here is what is done", "finished, merged into main at T",
  "we two decided Y". It is also how an agent discovers a *sleeping*
  workspace whose work interferes with its own.

Researched alternatives (2026-08-27): the native feature covers live
messaging completely and should not be duplicated; community buses
(claude-code-inter-session, claude-peers-mcp) are live transports;
Agent-MCP-class frameworks are orchestration, a different layer. No existing
tool provides a durable, workspace-scoped announcements log.

## Design center

**Announcements, not chatter.** The board carries only announcements — rare,
imperative, a headline plus bullets. Because they are rare they are
delivered *in full* to *every* workspace, automatically, before the agent
acts. Discussion between agents happens privately (native messaging); only
its outcome is announced, once, by one agreed signer. Relevance is judged by
the reader: an agent's own context decides better than any repo-based
filter could (repos relate in ways the config cannot know), and the fleet is
small (a human manages 5–10 workspaces in parallel).

## Decided behaviors

| # | Decision | Choice |
|---|----------|--------|
| 1 | Scope | ONE board per workspaces root. Every workspace (including `TRY-*`, adopted, nested) posts to it and reads all of it. No per-project boards, no repo-based filtering. |
| 2 | Storage | `<root>/.comm.jsonl`, one JSON record per line, time order: `{"ts": RFC3339Nano, "from": <workspace name>, "text": <string>}`. No id field, no channel field (channels are postponed and undesigned; JSONL grows fields for free). |
| 3 | Ordering | `ts` is strictly increasing within the file, enforced under the lock: a record whose clock time is ≤ the last record's `ts` is stamped last+1ns. Timestamps never reset, so a cursor is never invalidated by pruning (integer ids would restart at 1 after a full prune — rejected for exactly that bug). |
| 4 | Writes | Every write (put, prune) runs under `alloc.WithLock` — the SAME `<root>/.lock` the registry uses — as load → drop records older than the window → append → atomic save (temp file in root, fsync, rename; the `alloc.Save` shape). One code path; the file is never observed half-written. Reads take no lock. |
| 5 | Retention | Window = 30 days, one `const`, no config knob. `put` prunes in its own write; `comm prune` and `gc` prune explicitly (gc is irregular in practice — nothing depends on it). |
| 6 | Cursor | Per WORKSPACE: `<ws>/.workspace/comm-cursor` holding the `ts` of the last delivered record (RFC3339Nano, one line). Written only by `get --new`, via temp+rename. Missing or unparsable → zero (deliver the whole window). Lives in the pids dir: tool-owned, dies with the workspace, never in the registry. Sessions in one workspace share it (a second simultaneous session misses what the first consumed — accepted; `get`, `--since` and the Status note cover it). |
| 7 | Sender | Always the workspace containing cwd (`workspaceAt`, deepest match). No `--from`: the human already has a better channel (the session), and an agent cannot misidentify itself. |
| 8 | Delivery | Payload, not pointer: hooks run `workspace comm get --new` and inject its output — the full announcement text. Empty output ⇒ the hook is silent. Guard: when more than 10 records qualify, `--new` prints HEADLINES only (`<time>  <from>: <first line>`) plus a hint to `comm get --since`. |
| 9 | Delivery points | SessionStart (existing script, new block), UserPromptSubmit and PreToolUse (new script `comm-deliver.sh`, matcher: all tools). Rationale: an agent can only affect code through tools, so PreToolUse is "before you act, here is the news"; UserPromptSubmit covers an agent in a tool-less design chat; SessionStart covers re-entry. Implementation check: PreToolUse `additionalContext` support; fallback PostToolUse (loses only the first action after a post). |
| 10 | Doctrine amendment | *Hooks add context and may record what they delivered (the comm cursor); they never create, start or modify a workspace.* Written here and in the hook script header. Hooks always exit 0 and print nothing when the binary is absent or cwd is outside a workspace. |
| 11 | Own posts | `get --new` excludes records whose `from` is this workspace. Plain `get` shows everything. |
| 12 | Where commands work | `get` (window / `--since` / `--grep`), `listen`, `prune`: anywhere (the board is root-global). `put`, `get --new`: inside a workspace only — outside → exit 3 `not inside a workspace`. |
| 13 | Size guard | `put` refuses empty text and text over 4096 bytes: exit 2, message names the rule ("an announcement is a headline and bullets — point at the branch instead"). Every announcement lands in every sibling's context; the cap backs the skill's brevity rule. |
| 14 | Corrupt data | A malformed log line is skipped with one stderr warning naming the line number — never fatal (a bricked board would fail every hook and every `put`). `doctor` reports it as a finding. An unparsable cursor reads as zero; `doctor` notes it. |
| 15 | Lifecycle | `destroy`: the cursor dies with `.workspace/`; the workspace's announcements stay until the window prunes them (history; `ls -a` still explains the name). The tool never posts on its own — the board stays agent-authored. `release`/`adopt`: a stale cursor is a timestamp, so it stays correct. `gc`: prunes; the board never influences gc's gates. `uninstall`: removes the hook script (manifest); log and cursors are user data in the root and stay. |
| 16 | Output | `get`: header `<YYYY-MM-DD HH:MM>  <from>`, the text indented two spaces, blank line between records, oldest first. `--json`: the raw records as an array. `put`: prints `posted` (and the timestamp) on success. |
| 17 | Abstraction | `internal/comm` exposes a `Store` interface (append, list, prune) with the JSONL implementation behind it; the CLI, hooks and tests depend on the interface. Named revisit trigger for a real database (sqlite via a pure-Go driver): the channels design, if it turns out relational. |
| 18 | Skill | New section *The announcements board* absorbs *Coordinating with sibling sessions* as its live half. Templates: started / paused / finished (with merge target and time) / joint decision; first line is the headline; concrete file/class names (they are what `--grep` finds); facts not instructions; discussion private, outcome announced once; read as peer intent, judge relevance yourself; a sleeping interfering workspace → `status <ws>`, its branch, `ListAgents`, ask the user to wake it. |
| 19 | Install | `comm-deliver.sh` joins the manifest. `install` prints the settings.json snippets for UserPromptSubmit and PreToolUse beside the SessionStart one; it never edits settings.json. |
| 20 | Version | v1.8.0. README gains a short "Coordination" paragraph linking to `docs/reference.md`, which gets the full contract; CHANGELOG entry under Unreleased with each landing commit. |

## Command surface

```
workspace comm put <text…>            post; stdin when no args (or "-")
workspace comm get                    the 30-day window, oldest first
workspace comm get --new              deliver: unread ∧ in window ∧ not mine; advance cursor
workspace comm get --since <dur>      records newer than now-<dur> (30m, 3d, 2w); ignores the window
workspace comm get --grep <regexp>    case-insensitive match over from+text; combinable with --since
workspace comm get --json             any of the above as JSON
workspace comm listen                 last 20, then follow (re-list every second through the Store,
                                      print records newer than the last shown) until Ctrl-C
workspace comm prune                  drop records outside the window
```

Exit codes: 0; 1 I/O or lock failure; 2 usage (empty/oversized text, bad
flag); 3 `put`/`get --new` outside a workspace; 4 broken config (as every
command, via `loadRoot`). Completion: the subcommand names; `completeNothing`
for arguments.

## Mechanics

### `internal/comm` (new; depends on `alloc` for `WithLock` only)

```go
// Record is one announcement. TS is RFC 3339 with nanoseconds and strictly
// increasing within a board.
type Record struct {
    TS   time.Time `json:"ts"`
    From string    `json:"from"`
    Text string    `json:"text"`
}

// Store is the board. The JSONL file is one implementation; nothing above
// this interface knows about files.
type Store interface {
    // Append posts text as from, stamped now (or last+1ns if now ≤ last),
    // pruning records older than Window in the same write. Returns the
    // record as stored.
    Append(from, text string, now time.Time) (Record, error)
    // List returns every parseable record, oldest first. Malformed lines
    // are reported through warn (line number + reason) and skipped.
    List(warn func(string)) ([]Record, error)
    // Prune drops records older than now-Window.
    Prune(now time.Time) error
}

const Window = 30 * 24 * time.Hour
const MaxText = 4096
const HeadlinesOver = 10

func Open(root string) Store                   // the JSONL store at <root>/.comm.jsonl
func ReadCursor(wsDir string) time.Time        // zero when missing/unparsable
func WriteCursor(wsDir string, ts time.Time) error
func Unread(recs []Record, cursor time.Time, self string, now time.Time) []Record
func Since(recs []Record, cutoff time.Time) []Record
func Grep(recs []Record, re *regexp.Regexp) []Record
func Headline(r Record) string                 // first line of Text
func Follow(ctx context.Context, s Store, w io.Writer, interval time.Duration) error
```

Filtering is on `[]Record` in memory: the whole window is a few hundred
records at most; an index would be slower than the loop.

### `internal/cli/comm.go`

Cobra wiring only: parse flags, `loadRoot`, `workspaceAt(cwd)` where
required, call the package, format. Formatting lives here (comm never
formats), matching the `ls`/`status` convention.

### Hooks

- `assets/hooks/session-start.sh`: after the status block, if
  `workspace comm get --new` prints anything, emit
  `## Announcements from other workspaces` and the output.
- `assets/hooks/comm-deliver.sh`: gate (`command -v workspace`), extract the
  event name from the hook's stdin JSON, and exec
  `workspace comm get --new --hook <event>`. The BINARY emits the hook JSON
  `{"hookSpecificOutput":{"hookEventName":"<event>","additionalContext":"<text>"}}`
  via encoding/json — announcement text never passes through shell escaping.
  Silent success when nothing is unread. Exit 0 on every path.

### `doctor`

Two new kinds: `comm_corrupt_line` (finding; line number) and
`comm_cursor_unparsable` (note; workspace). Both root-level passes, cheap.

## Testing

- `internal/comm`: table tests — Append stamps strictly increasing TS
  (same-nanosecond and clock-backwards cases are **mutation-checked pins**);
  Prune by age; List skips a corrupt line and reports its number; Unread
  (cursor, window, self-exclusion), Since, Grep; cursor round trip and
  missing → zero. One `-race` test: N goroutines appending under the lock →
  N records, strictly ordered.
- `Follow` tested in Go with a millisecond interval: append while following,
  cancel, assert the appended record was printed once.
- txtar: put inside/outside; `get --new` delivers once then prints nothing
  (cursor pin); excludes own posts; headline fallback at 11 records;
  `--since`, `--grep`, `--json`; size guard; corrupt line → warning +
  `doctor` finding. Exact exit codes pinned in Go tests.
- Hook scripts run under the PATH shim inside and outside a workspace:
  silent + exit 0 outside, JSON with the announcement inside.

## Out of scope (recorded so they are not re-litigated by accident)

- **Channels / group discussion.** Native messaging is 1:1; three agents
  negotiating today means N copies. Real gap, needs its own design after the
  board exists. Separate card.
- **Pushing into Claude Code session sockets from `put`.** Undocumented
  registration format; non-child messages may be held for approval; would
  break silently on upgrade. Revisit if a third-party messaging API is
  documented — it would slot beside the hooks without touching the log.
- **Personas / display names.** Dropped entirely: `from` is the workspace
  name; role clarity is the identity that matters.
- **Repo-scoped delivery, per-project boards, config knobs for window and
  caps, `--from`.**

## §9 Build format

- Claude: this spec; the plan; the `internal/comm` skeleton (types, `Store`,
  every signature above with its contract as a doc comment) and the failing
  tests; `cli/comm.go` wiring; hook scripts, install wiring, `doctor` kinds,
  skill text, README/reference/CHANGELOG.
- cat: first gear — the bodies of `internal/comm` (JSONL load/save, Append
  with the TS rule, Prune, filters, cursor) until the unit tests pass.
  Second gear, description-first — `Follow`: Claude states the contract and
  the test, cat designs the loop.
- Reviews as for any implementer, plus the independent pass before landing.
  Land continuously to both remotes; tag v1.8.0 when the whole feature is in
  the daily driver.
