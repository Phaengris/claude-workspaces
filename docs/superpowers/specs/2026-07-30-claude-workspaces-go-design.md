# claude-workspaces-go — design

*2026-07-30. Validated in a brainstorming session against the v1 Ruby tool's
July 2026 review cycle (`claude-workspaces/docs/rewrite/00–05`). This document
is self-contained: it names every v1 concept it inherits, so it can be
implemented without reading the Ruby source.*

## 1. Identity and goals

A **new tool**, designed and written from scratch in Go: an *environment
engine* that provisions N isolated, runnable instances of a stateful dev stack
on one machine — worktrees, port/resource allocation, per-instance env,
service lifecycle — primarily for parallel Claude Code sessions, usable
without them.

**Goals**

1. Single static binary (`CGO_ENABLED=0`), no language runtime on the host,
   fast startup (completions and `workspace cd` run constantly).
2. Derived state over recorded state: the only registry is the allocations
   file; everything else is computed by looking at reality. No status machine,
   no "broken workspace" concept — every operation is an idempotent ensure
   that converges on re-run.
3. Learning vehicle: small packages with one purpose each, work split into
   short reviewable tasks, tests first.
4. Clean-room shaped: behavior comes from this design and from the v1 tool's
   *documented, observable* behavior (README, rspec cases as behavioral
   oracle). No code translation. New on-disk formats. New command surface.
   (Structural support for the "new tool" framing; not legal advice.)

**Non-goals**

- Migration of live v1 workspaces. Existing ones finish on Ruby; new ones are
  created here. The v1 tool remains untouched as fallback.
- Windows. POSIX-only (flock, process groups, `$SHELL -lc`). Linux first;
  macOS expected to work.
- Docker. The compose-based architecture explored in v1's planning
  (`05-docker-architecture.md`) stays a possible future; this tool runs
  everything on the host.
- v1 commands dropped below stay dropped until daily use proves them missed.

**Naming.** Binary: `workspace` (muscle memory, shell wrappers). Module:
`git.internal/cat/claude-workspaces-go`. Renaming the module for a later
open-source home is one `go.mod` line plus an import rewrite — deliberately
cheap, deliberately deferred.

## 2. Command surface

| Group | Command | Behavior |
|---|---|---|
| Lifecycle | `new <task_id> <desc> [project…]` | Allocate + create workspace dir + write `WORKSPACE.md` + checkout listed projects. **Transactional**: on any failure, undo everything this invocation created. |
| | `checkout <ws> <project…>` | Add project(s): worktree add, write `.env`, run setup (stamped), refresh `WORKSPACE.md`. Idempotent. |
| | `destroy <ws>` | down + run `teardown` commands + remove the workspace dir + release allocation. Only removes dirs the tool created. |
| Allocation | `adopt [dir] [--projects a,b]` | Provision env for an existing checkout the tool didn't create: allocate an index, write `.env`, detect projects via git worktree metadata (or `--projects`). Never claims ownership of the dir. Idempotent. |
| | `release [dir]` | Free the allocation. Never deletes a dir. |
| | `gc [--destroy-dirs]` | Release allocations whose dirs vanished; delete stale pid files. `--destroy-dirs` additionally destroys tool-created dirs whose worktree branches are fully merged. |
| Services | `up <ws> [target…]` | Ensure setup current (stamp hash) → start daemons not already running, in dependency order. Idempotent. |
| | `down <ws> [target…]` | Stop daemons (group TERM → poll ≤5s → KILL), report which signal was needed. |
| | `restart <ws> [target…]` | down + confirmed death + up for the targets; no targets = all daemons. |
| | `logs <ws> [daemon]` | Tail daemon logs from `.workspace/logs/`. |
| | `exec <ws> [project] <cmd…>` | Process-replace (`execve`) into cmd with workspace env **through the curated environment** (fixes v1's known exec env-poisoning bug by construction). |
| | `browse <ws> [project]` | Open the project's `browse_port` URL in the browser. |
| Observe | `ls` | All workspaces, one line each; `-g` adds git stats (computed concurrently). |
| | `status [ws]` | Detail view; every field derived live (worktrees, stamps, pid liveness). |
| | `env <ws> [project]` | Print resolved environment. |
| | `ports` | Allocated values overview across workspaces. |
| Sessions | `claude <ws> [-S] [-R] [args…]` | Launch Claude Code in the workspace dir with flag injection (§8). |
| | `launch <task_id> [<desc> [project…]] [-S] [-R] [-- args…]` | new + checkout + up + claude, one shot. Reuses the workspace if it already exists. |
| Navigate | `cd <ws> [project]` | Print target dir; the installed shell wrapper performs the actual cd. |
| | `which` | Detect workspace from cwd. |
| Meta | `doctor` | Config validation report + stale allocations + orphan daemons + port collisions. |
| | `install` | Copy running binary to `~/.local/bin/`, install shell wrapper + generated completions + Claude Code skill + SessionStart hook, create root dir + config stub. Writes a manifest of everything it placed. Idempotent. |
| | `uninstall` | Remove exactly what the install manifest lists. Never touches the workspaces root, config, or live workspaces. |
| | `completion <shell>` | Generated by the CLI framework (fish/bash/zsh); dynamic completion for workspace ids, project names (from loaded config — templated projects complete correctly), daemon names. |

**Service targets.** `up`/`down`/`restart` share one grammar: each `target`
is a project name or a daemon name, resolved against the workspace's
checked-out projects first, then their daemons. A daemon name defined by more
than one project is ambiguous — error, qualify as `project:daemon`.
Addressing a single daemon still runs the owning project's ensure-chain
(setup stamp) first. No targets = the whole workspace.

Aliases: `start`→`up`, `stop`→`down`. Global flags: `--json` on all query
commands (`ls`, `status`, `env`, `ports`, `which`), `--version`.

**Dropped from v1** (rationale in v1's `04-v2-vision.md`): `archive`/
`unarchive` (a workspace with daemons down and allocation released *is*
archived; re-provisioning is `adopt`), `resolve` (no broken state), `setup`
as a command (an ensure-step inside `checkout` and `up`), `title`, `welcome`
(short intro folded into `--help`), `prune` (→ `gc`).

**Workspace identifier**: full dir name or task id, as v1.

## 3. State model — derive, don't record

| Fact | Source of truth |
|---|---|
| dir ↔ index, task id, description, created_at, adopted? | `<root>/.allocations.json` — the **only** registry |
| project checked out | the worktree exists and git confirms it |
| setup done & current | stamp file `.workspace/setup-<project>.ok` containing a SHA-256 of the project's rendered setup commands — config change ⇒ stamp mismatch ⇒ `up` re-runs setup |
| daemon running | pid file `.workspace/pids/<name>` holding `<pid> <starttime>`; alive ⇔ pid exists **and** starttime matches `/proc/<pid>/stat` field 22 (pid-reuse-proof; macOS: pid-only fallback) |
| broken | no such state; operations are idempotent ensures |

`.allocations.json`: `{ "<abs dir>": { "index": 3, "task_id": "FIZZY-123",
"description": "…", "created_at": "…", "adopted": false } }` — guarded by
flock on `<root>/.lock`, written atomically (temp file + fsync + rename).
Index assignment fills gaps (lowest free index).

Placement and format are deliberate:

- **Per-root, not XDG.** Allocation indices only mean something relative to
  one root's workspace set. `CLAUDE_WORKSPACES_ROOT_DIR` therefore yields
  fully independent universes — which is also how the test suite isolates
  itself (tmpdir root ⇒ own registry). A global registry in
  `~/.local/share` would entangle roots and break that. Dot-prefixed to
  signal machine-owned: `ls <root>` shows only workspaces and `config.yml`.
- **JSON, not YAML.** `encoding/json` is the standard library — zero added
  dependencies (YAML is the third-party format here, needed for config
  regardless). Machine-written, machine-read state wants the unambiguous
  format; human-edited config wants the pleasant one.

Timestamps are RFC 3339 strings. The format is this tool's own — no v1
compatibility constraint.

## 4. Configuration

`<root>/config.yml`, YAML (kept over TOML: the schema is nested and
hand-edited; the start-entry idiom has no compact TOML form). Decoded
**strictly** — unknown keys are errors with line/column positions.
Validation runs at every load; `doctor` prints the full report.

Schema (v1-compatible so the existing config carries over; **no
`command_runner`** — see §6):

```yaml
values:                      # index-derived per-workspace values
  PORT: { start: 5000, per_workspace: 10 }   # → PORT0..PORT9

env:                         # global env for all projects
  RAILS_ENV: development

env_allow: [MY_TOKEN]        # extra parent vars let through the curated
                             # environment (§6); also valid per project

templates:                   # reusable project definitions
  client:
    params: [NAME]           # substituted at load; runtime tokens pass through
    repo: ~/dev/clients/${NAME}
    ...

projects:
  my-app:
    repo: ~/dev/my-app       # tilde-expanded
    base_branch: main
    path: my-app             # optional subdir name inside the workspace
    depends: other-project   # string or list; orders setup/up
    setup: [bundle install, bin/rails db:prepare]
    start:
      - echo "bare string = run-and-wait command"
      - rails: bin/rails s -p ${PORT0}        # name: cmd = daemon
    stop: []                 # optional extra stop commands
    teardown: [dropdb --if-exists ${DB_NAME}]
    env: { DB_NAME: my-app_${WORKSPACE}_development }
    browse_port: ${PORT0}
    instructions: |          # appended to WORKSPACE.md
      ...
  a-client: { template: client, params: { NAME: acme } }
```

Rules carried from v1's documented behavior:

- **Templates**: project keys shallow-merge over the template; `${PARAM}`
  substitution is load-time and only for names declared in `params:`;
  unknown template, missing param, undeclared param ⇒ validation error.
  Runtime tokens (`${WORKSPACE}`, `${PROJECT}`, `${PORT0}`…) pass through
  untouched.
- **Runtime substitution** applies in `env`, `setup`, `start`, `stop`,
  `teardown`, `browse_port`.
- **`.env` seeding**: at checkout, the source repo's `.env` is read as
  defaults and workspace env is merged on top; result written to the
  worktree's `.env`.
- **Start entries**: a YAML string is a run-and-wait command; a single-key
  map is a named daemon. One custom unmarshaler owns this distinction.
- Ordering of user-visible output (env files, project lists) is sorted
  alphabetically — declared as the contract, not insertion order.

## 5. WORKSPACE.md, not CLAUDE.md surgery

Workspace creation/checkout writes `WORKSPACE.md` (identity, checked-out
projects, ports/values, per-project instructions) and creates `CLAUDE.md`
once with a single reference line to it. Regeneration only ever rewrites
`WORKSPACE.md`, so agent notes accumulated in `CLAUDE.md` are never touched
(v1 lost them on regeneration).

## 6. Environment curation — the shims model

No per-command version-manager wrapper. Three pure mechanisms
(v1 `env_tools` behavior, post-`3c17215`):

1. **Allowlist**: spawned processes receive only a fixed set of safe parent
   vars (`HOME USER LOGNAME SHELL TERM TERM_PROGRAM LANG LANGUAGE LC_*
   TZ DISPLAY WAYLAND_DISPLAY XAUTHORITY SSH_AUTH_SOCK SSH_AGENT_PID
   GPG_AGENT_INFO GNUPGHOME XDG_* DBUS_SESSION_BUS_ADDRESS`) plus workspace/
   project env on top. Version-manager pin vars are excluded by *prefix*
   (`RBENV_ PYENV_ NODENV_ PLENV_ GOENV_ RUBYENV_ ASDF_ MISE_ __MISE_`).
2. **Sanitized PATH pass-through**: PATH survives, minus segments that are
   concrete per-version install bins — contains `/versions/` or `/installs/`
   and ends in `/bin` — so version-manager *shims* stay reachable and resolve
   each worktree's own `.ruby-version`/`.tool-versions` by cwd. Fail-safe:
   over-keeping only reproduces pin-to-launch-version, never worse.
3. **Startup self-sanitize**: applied to the tool's own process env before
   any command runs, so even inherit-spawns are clean by default.

**Every** spawn path uses this environment — setup/start/stop/teardown,
`exec`, `claude` — closing v1's documented gap where `exec` leaked the parent
env (`2026-05-11-cmd-exec-env-poisoning-design.md`).

**Escape hatch**: `env_allow` in config (global and per-project) appends
parent vars to the allowlist — the documented answer when a needed var
doesn't propagate.

**User-facing documentation is part of the feature.** The curated
environment is a compromise (any solution here is); the README must explain
the mechanism and its limits so behavior is never mysterious:

- a parent var not on the allowlist silently does not reach spawned
  commands — the fix is `env_allow` or a workspace `env` entry;
- the PATH-stripping heuristic recognizes common version-manager layouts
  (`…/versions/<v>/bin`, `…/installs/<tool>/<v>/bin`); an unrecognized
  layout degrades to v0 behavior — commands pin to the launch-time version —
  never anything worse;
- commands run under `$SHELL -lc`, so the login shell's own init can
  reintroduce environment; that is outside the tool's control.

In Go this is the natural idiom: `exec.Cmd.Env` is the complete child
environment; nothing is inherited unless put there.

## 7. Process layer

- **Spawn contract**: `$SHELL -lc "<command>"` (fallback `/bin/sh`), cwd =
  project worktree, env per §6. Run-and-wait commands capture stdout+stderr;
  a failure error carries the first stderr line.
- **Daemons**: own process group (`Setpgid`), stdout/stderr to
  `.workspace/logs/<name>.log` / `<name>.err.log` (truncated per start),
  pid file per §3.
- **Stop**: SIGTERM to the process *group*, poll liveness ≤5s, SIGKILL group,
  report the outcome. `restart` waits for confirmed death.
- **Ordering**: `depends` gives a topological order over the workspace's
  checked-out projects (edges to non-checked-out projects are ignored);
  `up` follows it, `down` reverses it. Cycles are a config validation error.
- **git plumbing**: argv-form only (`git -C <dir> …`), never through a shell.
  `ls -g` stats run concurrently, bounded (~8 workers).

## 8. Claude integration

- **`claude`** runs Claude Code in the workspace dir (wait, inherited stdio)
  and injects, unless overridden:
  - `--dangerously-skip-permissions` — skipped when `-S`/
    `--claude-no-skip-permissions` given, or when the user passes their own
    `--permission-mode`/`--dangerously-skip-permissions`.
  - `--continue` — only when a conversation exists for the workspace dir
    (Claude's project history probe); skipped with `-R`/`--claude-no-resume`,
    in print mode (`-p`), or when the user passes `-c`/`--continue`/`-r`/
    `--resume`/`--from-pr`.
  - Everything after `--` passes through verbatim. These two commands disable
    framework flag parsing and use a hand-written extractor (pure function,
    table-tested).
- **Skill** (embedded, installed by `install`): the session-facing interface —
  "work on FIZZY-123" → `new`/`checkout`/`up`, cd into the workspace.
- **SessionStart hook** (installed by `install`): context-only — if cwd is
  inside a managed workspace, print identity/env/status. Never mutates.

## 9. Architecture

```
claude-workspaces-go/
├── go.mod                        # module git.internal/cat/claude-workspaces-go
├── cmd/workspace/main.go         # thin: cli.Execute(), error → exit code
├── internal/
│   ├── cli/                      # cobra tree, one file per command
│   ├── config/                   # strict YAML load, templates, validation
│   ├── alloc/                    # allocations.json, flock, atomic write,
│   │                             #   index assignment, values computation
│   ├── wsp/                      # workspace ops: create/checkout/adopt/destroy
│   │                             #   (transactional), derived status, stamps,
│   │                             #   WORKSPACE.md, .env seeding
│   ├── proc/                     # daemons, pid+starttime, kill escalation
│   ├── envx/                     # allowlist, PATH sanitizer, self-sanitize
│   ├── gitx/                     # worktree add/remove, branch info, stats
│   └── ui/                       # human/--json rendering, tty detection
├── assets/                       # //go:embed: skill, shell wrappers, hook,
│                                 #   config stub
└── testdata/                     # testscript scripts + goldens
```

Dependency flow: `cli` → domain packages → leaves (`envx`, `gitx`, `ui`
depend on nothing internal). Config is loaded once and passed down
explicitly — no package-level singletons.

Runtime deps: `spf13/cobra` (CLI + generated completions),
`goccy/go-yaml` (maintained YAML with position info), `golang.org/x/sys`
(flock), `golang.org/x/term`. Dev dep: `rogpeppe/go-internal/testscript`.
Go: current stable.

**Errors & exit codes**: sentinel error kinds mapped in `main.go` —
0 success, 1 command/operation failure, 2 usage, 3 workspace/project not
found, 4 config error. (v1 exits 1 for everything; scripts and the skill can
now distinguish.)

## 10. Testing

TDD per task, two layers:

1. **Table tests** for pure logic: template expansion, values math, start-
   entry unmarshal, env allowlist + PATH sanitizer, claude-flag extraction/
   injection, index gap-filling, topo sort, tilde expansion. Where behavior
   carries over from v1, expected values are cribbed from its rspec *cases*
   (the oracle), never its code.
2. **testscript** for command flows: tmpdir with two throwaway git repos +
   config.yml, isolated via `CLAUDE_WORKSPACES_ROOT_DIR`; scripts assert
   stdout, exit codes, and file effects against goldens.

The version-resolution proof (two real rbenv Rubies resolving per-worktree)
is ported as an optional test that skips when rbenv isn't present.

## 11. Delivery: milestones and the learning workflow

Work is split into tasks of roughly one package or one command each — one
sitting to review. Each task brief states: scope, files, tests, **Go concepts
introduced** (the syllabus line), and a complexity rating doubling as a model
recommendation (routine → Opus; subtle → Fable). Implementation is test-first;
nothing merges until the human review (line-by-line on request) is done.

| Milestone | Contents | Usable result |
|---|---|---|
| M0 | scaffold, config load+validate, alloc, envx | `doctor` validates the real config.yml |
| M1 | read-only commands: ls, status, env, ports, which, cd | inspection works against Go-created fixtures |
| M2 | new/checkout/destroy, wsp writer, gitx | full create/destroy cycle |
| M3 | proc: up/down/restart/logs/exec, browse | daily-drivable for one real project |
| M4 | claude/launch, adopt/release/gc | parallel-session workflow complete |
| M5 | doctor (full), install/uninstall, completions, skill, hook | fresh-machine install; switch daily driving |

Deferred (explicitly not in v1.0 of this tool): profiles (named start
subsets, e.g. `test` = DBs only), Docker runtime seam, any dropped v1
command.
