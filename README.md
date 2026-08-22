# claude-workspaces (`workspace`)

[![CI](https://github.com/Phaengris/claude-workspaces/actions/workflows/ci.yml/badge.svg)](https://github.com/Phaengris/claude-workspaces/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

`workspace` runs several copies of your dev stack on one machine — one per
task — without containers.

Each workspace gets:

- a git worktree per project, on its own branch;
- its own ports and numbered values, derived from an allocated index;
- its own generated environment;
- its own daemons, logs, and lifecycle.

Two workspaces never fight over port 3000, a database, or a checkout. Built
for running several Claude Code sessions in parallel; equally usable without
them.

The tool records almost nothing. Branches, setup freshness, running daemons —
derived live from git, the filesystem, and `/proc`. No status machine, no
"broken" state: every command converges when re-run.

One static binary. Linux first; macOS builds and is expected to work, with
lighter liveness checks.

Details and reasoning live in [`docs/reference.md`](docs/reference.md); this
file is the short version.

---

## Separation, not virtualization

How are workspaces isolated? **They aren't — they are separated.** A workspace
is ordinary processes in ordinary directories. No container, no VM, no
namespace. Nothing collides because everything collidable is routed through
the allocated values: the server listens on `${PORT0}`, the database is
`my_app_${WORKSPACE}`, and each workspace gets its own numbers and its own
name.

That means the separation is exactly as complete as **your config** makes it.
The tool guarantees no two live workspaces share an allocation. It cannot know
that your app hardcodes port 3000 somewhere. If two workspaces fight over a
resource, route that resource through `values`/`env` — the starter config and
[`examples/rails.md`](examples/rails.md) show the patterns.

Why not real isolation? Because bare metal is simple and fast: no images, no
volume mounts, identical on Linux and macOS — and native processes mean native
tooling. Your debugger attaches. Version-manager shims resolve each worktree's
own `.ruby-version`. A container-based flavor is on the roadmap; today's trade
is deliberate.

**Isn't this built into Claude Code?** Partly — and the parts compose. Claude
Code's worktree isolation gives a session its own *checkout*. This tool owns
everything around the checkout: ports, env, databases-by-name, daemons, and a
durable task identity that outlives any one session. A session inside a
workspace can still spawn worktree-isolated subagents — code isolation nested
inside environment separation.

---

## Install

```sh
go install github.com/Phaengris/claude-workspaces/cmd/workspace@latest
workspace install
```

(A go-install'd binary reports version `dev` — `go install` cannot apply
`-ldflags`. To build from a checkout: `CGO_ENABLED=0 go build -o workspace
./cmd/workspace`, plus `-ldflags "-X
github.com/Phaengris/claude-workspaces/internal/cli.version=X.Y.Z"` if the
version matters. Prerequisites: a Go toolchain and `git`.)

`workspace install` copies the running binary and writes:

| Path | What |
|---|---|
| `~/.local/bin/workspace` | the binary |
| `~/.config/fish/functions/workspace.fish` | fish `cd` wrapper (autoloaded) |
| `~/.config/fish/completions/workspace.fish` | fish completions (autoloaded) |
| `~/.local/share/workspace/shell/workspace.bash` | bash/zsh `cd` wrapper — *you* source it |
| `~/.local/share/workspace/completions/…` | bash/zsh completions — *you* wire them |
| `~/.claude/skills/claude-workspaces/SKILL.md` | the Claude Code skill |
| `~/.local/share/workspace/hooks/session-start.sh` | the SessionStart hook |
| `~/.local/share/workspace/install-manifest.json` | the uninstall contract |
| `<root>/config.yml` | a commented starter config, only if absent |
| `<root>/` | the workspaces root, created if missing |

Everything is embedded in the binary; completions are generated from the live
command tree at install time. Re-running `install` is idempotent, and your
`config.yml` is never overwritten.

`install` **edits nothing that is yours** — no `settings.json`, no shell rc.
It prints what to add instead:

- the SessionStart hook entry for `~/.claude/settings.json` (the hook prints
  workspace identity and status into every session that starts inside a
  workspace; it never mutates anything);
- the shell lines for `~/.bashrc` / `~/.zshrc` (fish needs nothing). The
  wrapper exists because `workspace cd` can only *print* a directory — a child
  process cannot chdir your shell.

Make sure `~/.local/bin` is on your `PATH`. Re-installing *from* the
installed binary is safe (the same-file case is detected).

`workspace uninstall` removes exactly what the manifest lists — never the
root, your config, or your workspaces — and tells you what it left behind,
including the SessionStart entry in `settings.json` (not ours to remove,
since it was not ours to add). A manifest entry pointing at `/`, `$HOME`, or
the workspaces root is refused rather than executed. With nothing installed
it prints `nothing installed` and exits 0.

---

## Quick start

The root is `~/claude-workspaces` (override: `CLAUDE_WORKSPACES_ROOT_DIR`;
each root is an independent universe). Describe a project in
`<root>/config.yml`:

```yaml
values:
  PORT:
    start: 5000
    per_workspace: 10

projects:
  demo:
    repo: ~/dev/demo
    setup:
      - echo "setup ran"
    start:
      - echo "migrations go here"
      - web: python3 -u -m http.server ${PORT0}
    teardown:
      - echo "teardown ran"
    env:
      DEMO_URL: http://localhost:${PORT0}
    browse_port: ${PORT0}
```

Then:

```sh
workspace doctor
workspace new DEMO-1 "try the tool" demo
workspace up DEMO-1
workspace status DEMO-1
workspace logs DEMO-1 web -n 20
workspace cd DEMO-1
workspace claude DEMO-1
workspace down DEMO-1
cd ~
workspace destroy DEMO-1
```

What happened: `new` allocated index 0 (so `PORT0..PORT9` = 5000..5009),
created the dir, checked out a `demo` worktree on branch `DEMO-1_try-the-tool`,
wrote the `.env`, and ran `setup:` once (re-run only when its lines change).
`up` ran the bare `start:` entry to completion, then started the daemon.
`claude` opened Claude Code in the workspace. `destroy` ran `teardown:` and
removed everything, allocation included.

Notes: use `python3 -u` (and equivalents) — a daemon's stdout is a file, and
block-buffering makes `logs` unreadable otherwise (and a daemon started a
second ago may not have written anything yet — run `logs` again). And `cd ~` before
`destroy`: don't saw off the directory you're standing in.

---

## Configuration

One file: `<root>/config.yml`. Decoded **strictly** — unknown keys are errors
with `line:column` positions — and validated on every load. No reload step, no
cache.

Onboarding a real project is two halves: this file, and making the app
*consume* what the env provides (no hardcoded ports or database names).
[`examples/rails.md`](examples/rails.md) walks a Rails + Vite + Sidekiq app
through both — and the installed skill teaches sessions the same procedure, so
"configure workspaces for my project" is a thing you can ask a session to do.

The full key set, annotated:

```yaml
# Workspace at index i gets NAME0..NAME<k-1> = start + i*k + n
values:
  PORT:
    start: 5000
    per_workspace: 10

# environment for every project's commands, ${…} substituted
env:
  RAILS_ENV: development

# extra PARENT variables allowed through the curated environment
env_allow: [MY_API_TOKEN]

# reusable project definitions
templates:
  rails-client:
    params: [NAME]
    repo: ~/dev/clients/${NAME}
    start:
      - rails: bin/rails s -p ${PORT0}
    browse_port: ${PORT0}

projects:
  my-app:
    repo: ~/dev/my-app             # REQUIRED; ~ expanded
    base_branch: main              # branch off this (default: the repo's HEAD)
    path: my-app                   # subdir in the workspace (default: the key)
    depends: shared-lib            # orders setup/up; reversed for down
    setup:                         # at checkout; re-run when these lines change
      - bundle install
      - bin/rails db:prepare
    start:                         # what `up` runs, in order
      - bin/rails db:migrate       #   bare string  = run-and-wait
      - rails: bin/rails s -p ${PORT0}   # {name: cmd} = daemon
      - worker:                    # {name: {command, description}} = daemon
          command: bin/sidekiq     #   whose description status/WORKSPACE.md
          description: background jobs   # show, so sessions know what it's for
    stop:                          # optional; AFTER this project's daemons stop
      - bin/rails tmp:clear
    teardown:                      # on `destroy`, before the worktree goes
      - dropdb --if-exists my_app_${WORKSPACE}
    env:                           # project env, over the global env
      DATABASE_URL: postgres:///my_app_${WORKSPACE}
      PORT: ${PORT0}
    browse_port: ${PORT0}
    instructions: |                # appended verbatim to WORKSPACE.md
      Tests: `bin/rspec`.
  shared-lib:
    repo: ~/dev/shared-lib
  acme:
    template: rails-client
    params:
      NAME: acme
```

Runtime tokens: `${WORKSPACE}` (the task id), `${PROJECT}`, and the derived
values (`${PORT0}`, …) — substituted in `env`, `setup`, `start`, `stop`,
`teardown`, `browse_port`. Unknown tokens pass through.

**Two YAML caveats**, worth knowing before they bite:

- Unquoted `${…}` inside **flow style** is invalid YAML: `env: {A: ${PORT0}}`
  fails. Use block style (as above) or quote the value.
- A colon **followed by a space** inside a bare `start:` entry makes it a
  *map* — `- echo "run: it"` becomes a daemon named `echo "run`. Quote the
  whole entry whenever it contains `: ` (a colon without a space, as in URLs,
  is fine).

Deep semantics — template merging, values math, `depends`/`path` rules, `.env`
seeding, error positions — in [`docs/reference.md`](docs/reference.md#configuration-semantics).

---

## Commands

`<ws>` is a workspace identifier: the full directory name or the task id. An
ambiguous task id is a plain error listing the candidates.

| Group | Command | What it does |
|---|---|---|
| **Lifecycle** | `new <task_id> <description> [project…]` | Allocate, create, check projects out. Transactional: any failure undoes everything. |
| | `checkout <ws> <project…>` | Add projects: worktree, `.env`, stamped `setup`. Idempotent. |
| | `destroy [--force] <ws>` | `down` → `teardown` → remove → release. Only removes dirs the tool created. |
| **Allocation** | `adopt [dir] [--projects a,b]` | Give an existing directory an allocation and env. Never claims ownership; idempotent. Defaults to the cwd's workspace. |
| | `release [dir]` | Drop the allocation, touch no files. Defaults to the cwd's workspace. |
| | `gc [-d]` | Release vanished allocations, reap stale pid files; `-d` also destroys fully merged, clean, daemonless, tool-created workspaces. |
| **Services** | `up <ws> [target…]` (alias `start`) | Ensure setup, run the run-and-waits, start daemons. Idempotent. |
| | `down <ws> [target…]` (alias `stop`) | Stop daemons (group TERM → ≤5s → KILL), then run `stop:`. |
| | `restart <ws> [target…]` | `down` with confirmed death, then `up`. |
| | `logs <ws> <daemon> [-n N] [-f]` | One daemon's log; `-f` follows stdout+stderr. |
| | `exec <ws> [project] <cmd…>` | Replace this process with the command, under the curated environment. |
| | `browse <ws> [project]` | Open `http://localhost:<browse_port>` — after checking something actually listens; refuses dead ports. |
| **Observe** | `ls [-g] [-a]` | Every workspace, one line each. `-g`: `project@branch` cells, `*` = dirty. `-a`: also unallocated root dirs — released workspaces (identity from the dir name; `adopt` to reuse) and strangers, labeled `(unmanaged)`. |
| | `status [ws]` | One workspace in full, derived live — plus the `## Status` note sessions keep in the workspace's CLAUDE.md, so "where was I with this?" is answerable without opening a session. No argument: the `ls` listing. |
| | `env <ws> [project]` | The resolved environment. |
| | `ports` | Allocated value blocks across workspaces. |
| **Sessions** | `claude <ws> [-S] [-R] [args…]` | Claude Code in the workspace dir, with flag injection. |
| | `launch <task_id> [<description> [project…]] [-S] [-R] [-- args…]` | `new`-or-reuse + `checkout` + `claude`, one shot. Daemons are lazy — start them with `up`. |
| | `try <description…> [-S] [-R] [-- args…]` | A draft workspace: no projects, generated `TRY-<n>` id, the words are the description, straight into a session — allocated values included, so a scratch server has its ports. Graduate with `checkout`, or `destroy`. |
| **Navigate** | `cd <ws> [project]` | Print the directory; the shell wrapper does the `cd`. |
| | `which` | The workspace containing the cwd (exit 3 if none). |
| **Meta** | `doctor` | Health report: config, registry, repos, daemons, unregistered dirs. Reports, never fixes. |
| | `install` / `uninstall` | See *Install*. |
| | `completion <shell>` | Generated completion script (bash, zsh, fish). |

Three creation commands, one door: `new` scripts, `launch` is the daily
one-shot, `try` is the scratchpad.

Exit codes mean things: 0 ok, 1 failed, 2 usage, 3 not found, 4 config error.
`--json` on the query commands. Targets for `up`/`down`/`restart`/`logs` are
project names, daemon names, or `project:daemon`.
[More on the command surface.](docs/reference.md#command-surface-details)

---

## The environment, briefly

Commands the tool runs for you (`setup`, `start:`, `exec`, …) get a **curated**
environment: a fixed allowlist of parent variables, a PATH with per-version
bins stripped (so version-manager *shims* resolve each worktree's own
`.ruby-version`), and your `env:` on top. A variable not on the allowlist
doesn't arrive — `env_allow:` is the door.

Claude **sessions** get your real login environment instead (sanitized of
version pins), overlaid with the workspace's values. Two tiers, on purpose:
daemons need reproducibility, the operator's tool needs your actual setup.

The full model — the allowlist, the PATH heuristic, the login-shell caveats —
in [`docs/reference.md`](docs/reference.md#environment-curation--the-shims-model).
Read its *Limits* once; it will save you a confused hour.

---

## Services, briefly

`start:` entries are run-and-waits (bare strings — migrations, preludes) or
daemons (named — own process group, own logs, pid+starttime liveness that
survives pid reuse). **Daemons are lazy**: nothing starts them until
`workspace up`. Give them `description:`s — `status` shows those to every
session, which is how a session knows what to start.

`down` stops by group TERM with a KILL escalation and takes its no-target
inventory from the pid files on disk, not the config — a daemon you renamed
away still gets stopped. `stop:` runs *after* daemons are down.

The full lifecycle — liveness details, restart asymmetry, logs behavior — in
[`docs/reference.md`](docs/reference.md#services--daemons).

---

## Claude Code integration

This is the part built *for* running agent fleets:

- **The skill** (installed) teaches sessions the whole convention: create with
  `launch`/`try`, start only the daemons the task needs, verify against a
  running server, keep names short, and end substantial turns with a handoff
  report (done / needs you / watch out).
- **The SessionStart hook** prints the workspace's identity and live status
  into every session that opens inside one — including each daemon's
  description and the status note.
- **The status note**: sessions maintain a `## Status` section in the
  workspace's `CLAUDE.md` (the tool writes that file once and never touches it
  again). `workspace status` renders it, the hook delivers it — so both you
  and the next session re-enter a workspace in seconds, days later.
- **Sessions inject sensible flags**: `--dangerously-skip-permissions` and
  `--continue` (when history exists), each suppressible.
  [Injection rules and sharp edges.](docs/reference.md#sessions)

None of it requires Claude: every convention is a file or a CLI surface.

---

## Workspace hygiene

One rule runs through everything: **the tool never deletes a directory it did
not create.** `adopt` manages what you made; `release` un-manages without
touching files; `destroy` is transactional and aborts on any stop/teardown
failure; `gc -d` destroys only what is provably done — tool-created, merged,
clean, daemonless — and doubt always reads as "keep".

The exact gates, `--force`'s narrow meaning, and doctor's checks:
[`docs/reference.md`](docs/reference.md#workspace-hygiene).

---

## Development

```sh
go test ./... -count=1      # table tests + testscript command flows
gofmt -l .                  # must print nothing
go vet ./...
CGO_ENABLED=0 go build -ldflags "-X github.com/Phaengris/claude-workspaces/internal/cli.version=$(git describe --tags --always)" -o workspace ./cmd/workspace
```

Runtime dependencies: `spf13/cobra`, `goccy/go-yaml`, `golang.org/x/sys` (and
cobra's `pflag`). Test-only: `rogpeppe/go-internal/testscript`.

Layout: `cmd/workspace` is a thin main; `internal/cli` is the cobra tree, one
file per command; `config`, `alloc`, `wsp`, `proc` are the domain;
`envx`/`gitx`/`ui`/`xerr` are leaves. Assets are `//go:embed`ed. Tests are
table tests for pure logic plus `testscript` flows against real processes and
real git repos, isolated per-test by `CLAUDE_WORKSPACES_ROOT_DIR`.

---

## How this was built

This tool is itself a product of the workflow it serves. I designed and
specified it and own every product and architecture decision; the
implementation was AI-driven ([Claude Code](https://claude.com/claude-code),
end to end, a clean-room rewrite of an earlier private tool) under per-task
independent review. The paper trail is checked in — commit trailers say who
typed, the specs say who decided.

- **Spec first**: [`docs/superpowers/specs/`](docs/superpowers/specs/) — every
  decided behavior has a written row.
- **Milestone plans**: [`docs/superpowers/plans/`](docs/superpowers/plans/) —
  binding contracts, named test cases, decided-behaviors tables.
- **Subagent-driven execution**: fresh AI implementer per task, independent AI
  review per task, whole-branch reviews before merges.
- **TDD with mutation-checked pins**: load-bearing assertions are verified to
  fail when the behavior they pin breaks.
- **Human owner in the loop**: design decisions, reviews of reviews, and every
  release call.

Post-1.0 development runs the same loop in miniature, driven by daily use —
see the [CHANGELOG](CHANGELOG.md): most entries trace to a real moment at a
real terminal.
