# Lazy daemons + service descriptions — design

**Date:** 2026-08-11
**Status:** approved (brainstormed with cat; first real-use feedback after v1.0)
**Ships as:** v1.1.0

## Motivation

First real `workspace launch` after the v1.0 cutover started all four of the
project's daemons unasked. Most sessions touch one service or none; the rest
burn ports, CPU and log noise. The fix is two-sided:

1. **Lazy:** `launch` stops starting daemons. They start when someone —
   usually the Claude session itself — asks for them.
2. **Informed:** a session must know what daemons exist and *what each is
   for*, or it cannot know what to start. Today `status` (injected into every
   session by the SessionStart hook) lists daemons and live state but not
   their role.

Everything needed at the mechanism level already exists: `up` accepts
daemon-level targets (`up <ws> rails`, `up <ws> patternima:rails`), and
`status` already renders every configured daemon with derived running state.
This design only changes *when* things start and *what the reader is told*.

## Decided behaviors

| # | Decision | Choice |
|---|----------|--------|
| 1 | Lazy policy | `launch` NEVER auto-starts daemons. No per-daemon `lazy:` flag, no `-U` launch flag. `up` is one command away. |
| 2 | Where role knowledge lives | New optional per-daemon `description:` in config — single source; rendered into `status`, WORKSPACE.md and doctor. NOT `instructions:` prose (can't render per-daemon into status). |
| 3 | `up <ws>` (no targets) | Unchanged: starts everything checked out. It is the explicit "bring it all up". |
| 4 | launch-on-reuse convergence | Gone with the up phase: launch no longer restarts dead daemons on reuse. It leaves processes exactly as found. |
| 5 | Description `${}` | Rendered through the same runtime substitution as commands (`wsp.Subst` over `wsp.ResolvedEnv`), so readers see `localhost:20800`, not `${PORT_0}`. |
| 6 | Missing description | Doctor **note** (uncounted), never a finding — nothing is broken, and findings keep their "a command fixes this" contract. Reported once per `project:daemon`, not per workspace. |
| 7 | Run-and-waits | No descriptions — they are unnamed, untargetable and re-run on every `up`; nothing to lazily start or describe. |
| 8 | Version | v1.1.0 — behavior change (launch), backward-compatible config grammar. |

## 1. `launch`: remove the up phase

`newLaunchCmd` phase 2 (`wsp.ResolveTargets` + `upWork` over the whole
workspace) is deleted. launch becomes: create-or-reuse → checkout → session.

- `hintNothingCheckedOut` stays (a workspace with no projects still deserves
  the hint), now emitted from the resolve-free path — launch checks
  `ProjectStates` directly or keeps a no-target resolve *for the hint only*,
  whichever reads cleaner; it must not start anything either way.
- Help text (`Short`/`Long`) drops "start daemons" and says daemons start on
  demand via `up`.
- Everything else about launch — grammar, `-S`/`-R`, `--` passthrough, the
  description-slot note, abort-on-phase-failure — is untouched.

## 2. Config: per-daemon `description:`

`start:` entries gain a third accepted shape. All three:

```yaml
start:
  - bundle install                       # bare string: run-and-wait (unchanged)
  - worker: bundle exec sidekiq          # {name: cmd}: daemon (unchanged)
  - rails:                               # {name: {…}}: daemon with description
      command: bin/rails s -p ${PORT_0}
      description: app server — UI at http://localhost:${PORT_0}
```

- `config.StartEntry` gains `Description string`. `UnmarshalYAML` accepts, in
  order: bare string; `{name: string}`; `{name: {command, description}}`.
- Nested form: `command` **required** (empty/missing is a config error),
  `description` optional, any other key rejected (strict, like the rest of
  config). A map value that is neither string nor that struct is an error
  naming the entry.
- `wsp.Daemon` gains `Description string`; `DaemonsOf` carries it through.
- Validation additions live with the existing per-project daemon checks
  (errors.Join, all-at-once).
- goccy sharp edge stands: examples use block style (`${…}` in flow
  collections is rejected).

## 3. Surfacing

One authoring home (config), three read paths. All rendering substitutes
`${}` via `wsp.Subst(desc, wsp.ResolvedEnv(cfg, taskID, project, index))`.

**status** — the daemon line gains the description as a suffix:

```
    rails: stopped — app server — UI at http://localhost:20800
    worker: running (pid 1234)
```

No description → line unchanged (no trailing dash). JSON: `statusDaemon`
gains `"description"` with `omitempty`. Because the SessionStart hook prints
`status` verbatim, every session receives this unprompted — this is the
primary channel by which a session learns what it may start.

**WORKSPACE.md** — each checked-out project's section gains a services block
listing its daemons, descriptions, and the exact start command:

```
### services

- `rails` — app server — UI at http://localhost:20800
- `worker`

Daemons are not started automatically. Start what you need:
`workspace up <name> <daemon>`; logs: `workspace logs <name> <daemon>`.
```

(Exact wording is the implementer's; the block must name each daemon, show
its substituted description when present, and state the lazy convention with
the concrete `up` invocation.) `WriteWorkspaceMD` regenerates the file
wholesale already, so the block follows config edits on the next rewrite with
no new refresh machinery.

**SessionStart hook** (`assets/hooks/session-start.sh`) — one added line
after the existing management hint:

```
Daemons are not auto-started; start what you need with workspace up <ws> <daemon>.
```

## 4. Skill

`assets/skill/SKILL.md` teaches the convention (data stays out of the skill):
daemons are lazy; the session-start status block lists what exists, its live
state, and what each is for; start on demand with `up <ws> <daemon>`, watch
with `logs`, stop with `down`. Remove/adjust any wording that implies launch
brings services up.

## 5. Doctor

A config-level advisory pass, run once (not per workspace): for every
configured daemon with an empty `description`, emit a **note** on the
existing uncounted note channel:

```
note: daemon patternima:rails has no description — sessions only see its
      command; add description: under start: to say what it's for
```

Findings array and `doctor: N finding(s)` count are unaffected; exit codes
unchanged. In `--json`, notes ride whatever the note channel already emits.
Descriptions here are NOT `${}`-substituted (no workspace in scope; the raw
text is what the user edits anyway) — only presence is checked.

## 6. Docs + release

- README in the same commit: launch description, config reference gains the
  nested form, lazy convention documented, and the v1-divergences appendix
  gains the launch row (v1.0 launched-and-started; v1.1 launches lazy and no
  longer converges dead daemons on reuse).
- `config_stub.yml` asset shows the nested form on one daemon.
- Tag v1.1.0; rebuild + `./workspace install` on the real machine.

## Testing

Project conventions apply (TDD; table tests for pure logic; txtar for command
flows; mutation-check load-bearing pins):

- config: table tests for the three entry shapes, nested-form strictness
  (missing command, unknown key), description carried to `DaemonsOf`.
- launch txtar: launch creates + checks out and does NOT start daemons (no
  pid files, no "started" lines); reuse path leaves a dead daemon dead.
- status txtar: description suffix rendered substituted; no-description line
  unchanged; JSON field present/omitted.
- WORKSPACE.md: services block content via existing writer tests.
- doctor txtar: note emitted for description-less daemon, findings count
  unchanged, exit 0.
- hook line: asset content pinned wherever install tests pin it today.

## Out of scope (deliberate)

Per-daemon `lazy:` flags; a launch `-U` flag; auto-start-on-first-use;
descriptions on run-and-waits; any new stored state. Backlog remains
`docs/superpowers/plans/2026-08-10-m5-deferred-items.md`.
