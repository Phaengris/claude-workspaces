# chaban

[![CI](https://github.com/Phaengris/chaban/actions/workflows/ci.yml/badge.svg)](https://github.com/Phaengris/chaban/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

`chaban` runs several copies of your dev stack on one machine, one per task,
without containers. *Chaban* means shepherd: it keeps each task in its own
pen. ([Why the name](#why-chaban).)

Each copy is a workspace. Each workspace gets:

- a git worktree per project, on its own branch;
- its own ports and other numbered values;
- its own generated environment;
- its own daemons and logs.

So two workspaces never fight over port 3000, a database, or a checkout.

It is built for running several coding-agent sessions side by side, one per
workspace. chaban starts whatever agent you configure. It also has extras for
[Claude Code](https://claude.com/claude-code): a skill and a session-start
hook. You can use it with no agent at all, too.

chaban records almost nothing. Branches, whether setup is current, which
daemons run: all of it is read live from git, the filesystem and `/proc`.
There is no "broken" state to repair. Every command converges when you run it
again.

It is one static binary. Linux is the main platform. macOS builds and should
work, with simpler liveness checks.

This file is the short version. The details and the reasons behind them are in
[`docs/reference.md`](docs/reference.md).

---

## How workspaces are kept apart

A workspace is ordinary processes in ordinary directories: no container, no
VM, no namespace. Nothing collides because everything that could collide goes
through the allocated values. The server listens on `${PORT0}`, the database
is `my_app_${WORKSPACE}`, and each workspace gets its own numbers and its own
name.

So the separation is only as complete as your config makes it. chaban makes
sure no two live workspaces share an allocation. It can't know that your app
hardcodes port 3000 somewhere. If two workspaces fight over something, route
it through `values` and `env`. The starter config and
[`examples/rails.md`](examples/rails.md) show how.

### Why not containers

Plain processes are simple and fast. There are no images and no volume
mounts, and it works the same on Linux and macOS. Your debugger attaches.
Version-manager shims pick up each worktree's own `.ruby-version`. A
container-based flavor is on the roadmap, but today's trade is deliberate.

### Doesn't my agent already do worktrees?

Some do. Claude Code, for example, can give a session its own checkout. That
covers the code. chaban covers everything around it: ports, env, databases by
name, daemons, and a task identity that outlives any one session. The two work
together. A session inside a workspace can still start worktree-isolated
subagents.

---

## Install

```sh
CGO_ENABLED=0 go install github.com/Phaengris/chaban/v2/cmd/chaban@latest
chaban install
```

You need a Go toolchain and `git`. A binary from `go install` reports its
version as `dev`, because `go install` can't set `-ldflags`. To build from a
checkout with a real version:

```sh
CGO_ENABLED=0 go build -ldflags "-X github.com/Phaengris/chaban/v2/internal/cli.version=X.Y.Z" -o chaban ./cmd/chaban
```

### What install writes

`chaban install` copies the running binary and writes:

| Path | What |
|---|---|
| `~/.local/bin/chaban` | the binary |
| `~/.config/fish/functions/chaban.fish` | fish `cd` wrapper (autoloaded) |
| `~/.config/fish/completions/chaban.fish` | fish completions (autoloaded) |
| `~/.local/share/chaban/shell/chaban.bash` | bash/zsh `cd` wrapper (you source it) |
| `~/.local/share/chaban/completions/…` | bash/zsh completions (you wire them) |
| `~/.claude/skills/chaban/SKILL.md` | the Claude Code skill |
| `~/.local/share/chaban/hooks/session-start.sh` | the Claude Code SessionStart hook |
| `~/.local/share/chaban/install-manifest.json` | the list `uninstall` removes |
| `<root>/config.yml` | a commented starter config, only if absent |
| `<root>/` | the workspaces root, created if missing |

Everything comes from inside the binary. Completions are generated from the
live command tree. You can run `install` again at any time, and it never
overwrites your `config.yml`. Installing again from the installed binary
itself is safe too.

Make sure `~/.local/bin` is on your `PATH`.

### What you add yourself

`install` edits none of your files: no `settings.json`, no shell rc. It prints
what to add instead:

- The SessionStart hook entry for `~/.claude/settings.json`. Claude Code only;
  see [Claude Code extras](#claude-code-extras).
- The lines for `~/.bashrc` or `~/.zshrc`. fish needs nothing. The wrapper is
  there because `chaban cd` can only print a directory: a child process can't
  change your shell's directory.

### Uninstall

`chaban uninstall` removes exactly what the manifest lists. It never removes
the root, your config or your workspaces. It tells you what it left behind,
including the hook entry in `settings.json`: that one is yours to remove,
since you added it. The manifest lives in your home and anything could edit
it, so entries no install could have written are refused: a relative path,
`/`, `$HOME`, and the root or anything under it. With nothing installed, it
prints `nothing installed` and exits 0.

### Coming from claude-workspaces

chaban was called claude-workspaces before v2.0. If you used it,
[Upgrading from claude-workspaces](docs/upgrading-from-claude-workspaces.md)
moves your setup over.

---

## Quick start

The root is `~/workspaces`. Set `CHABAN_ROOT_DIR` to use another one; each
root is independent. Describe a project in `<root>/config.yml`:

```yaml
# 10 ports per workspace: workspace 0 gets PORT0..PORT9 = 5000..5009,
# workspace 1 gets 5010..5019, and so on.
values:
  PORT:
    start: 5000
    per_workspace: 10

# The command you'd type in a terminal to start your agent.
agent:
  start: claude

projects:
  demo:
    repo: ~/dev/demo               # a git repo you have; each workspace gets a worktree of it
    setup:                         # runs once, when the project is checked out
      - echo "setup ran"
    start:                         # what `chaban up` runs, in order
      - echo "migrations go here"  #   a plain line runs and finishes
      - web: python3 -u -m http.server ${PORT0}   # a named line is a daemon
    teardown:                      # runs on `chaban destroy`
      - echo "teardown ran"
    env:                           # for the project's commands, and written to its .env
      DEMO_URL: http://localhost:${PORT0}
    browse_port: ${PORT0}          # the port `chaban browse` opens
```

Then:

```sh
chaban doctor
chaban new DEMO-1 "try the tool" demo
chaban up DEMO-1
chaban status DEMO-1
chaban logs DEMO-1 web -n 20
chaban cd DEMO-1
chaban chat DEMO-1
chaban down DEMO-1
cd ~
chaban destroy DEMO-1
```

What happened:

- `new` allocated index 0, so `PORT0..PORT9` are 5000..5009. It created the
  directory, checked out a `demo` worktree on branch `DEMO-1_try-the-tool`,
  wrote the `.env` and ran `setup:`. `setup:` runs again only when its lines
  change.
- `up` ran the plain `start:` entry to completion, then started the `web`
  daemon.
- `chat` ran `claude` in the workspace directory. Put your own agent's
  command in `start`; see [Agents](#agents).
- `destroy` ran `teardown:`, removed the worktree and the directory, and
  released the allocation. The branch `DEMO-1_try-the-tool` stays in
  `~/dev/demo`: a branch is your work.

Two tips. Use `python3 -u` (or your language's equivalent): a daemon's stdout
is a file, and buffered output makes `logs` look empty. A daemon started a
second ago may not have written anything yet either, so run `logs` again. And
`cd ~` before `destroy`, so you aren't standing in the directory it removes.

### Day to day: `launch`

The steps above show each part on its own. Day to day, one command does it:

```sh
chaban launch DEMO-2 "fix the header" demo
```

It creates `DEMO-2` (or reuses it, if it exists), checks out `demo` and starts
your agent there. Daemons stay down until you or the agent run
`chaban up DEMO-2`. For an experiment with no project yet,
`chaban try "does this library fit"` makes a `TRY-<n>` workspace and starts
the agent in it.

---

## Configuration

Everything is in one file, `<root>/config.yml`. It is read strictly: an
unknown key is an error, with its `line:column`. Each command reads it fresh,
so there is no reload step.

Onboarding a real project has two halves: this file, and making the app use
what the env gives it (no hardcoded ports or database names).
[`examples/rails.md`](examples/rails.md) walks a Rails + Vite + Sidekiq app
through both. The installed skill teaches Claude Code sessions the same steps,
so you can ask a session to "configure workspaces for my project".

All the keys, with real values:

```yaml
# Numbered values per workspace. With start 5000 and 10 per workspace,
# workspace 0 gets PORT0..PORT9 = 5000..5009, workspace 1 gets 5010..5019.
# REDIS gives each workspace one Redis database: workspace 0 gets REDIS0 = 1.
values:
  PORT:
    start: 5000
    per_workspace: 10
  REDIS:
    start: 1
    per_workspace: 1

# Environment for every project's commands.
env:
  RAILS_ENV: development

# chaban starts with your shell's whole environment, but passes only a short
# list of variables to the commands it runs (HOME, LANG, SSH_AUTH_SOCK, ...),
# so a workspace doesn't pick up stray settings. Name here any other
# variable your commands need.
env_allow: [MY_API_TOKEN]

# How `chaban chat` starts your agent. Each line is a terminal command.
agent:
  start: claude                    # a new session
  resume: claude --continue        # continue the last session
  # chooses between the two: exit 0 runs `resume`, exit 1 runs `start`.
  # Your own script here; the built-in check for Claude Code is in Agents.
  can_resume: ~/.local/bin/claude-has-session-here.sh
  instructions_file: CLAUDE.md     # the file chaban writes for the agent (default AGENTS.md)

# Shared project definitions; ${NAME} is filled in by each project that uses it.
templates:
  rails-client:
    params: [NAME]
    repo: ~/dev/clients/${NAME}
    start:
      - rails: bin/rails s -p ${PORT0}
    browse_port: ${PORT0}

projects:
  my-app:
    repo: ~/dev/my-app             # required
    base_branch: main              # new branches start from here (default: the repo's HEAD)
    path: my-app                   # its directory inside the workspace (default: the key)
    depends: shared-lib            # set up and started after shared-lib, stopped before it
    setup:                         # runs at checkout, and again when these lines change
      - bundle install
      - bin/rails db:prepare
    start:                         # what `chaban up` runs, in order
      - bin/rails db:migrate       #   a plain line runs and finishes
      - rails: bin/rails s -p ${PORT0}   # a named line is a daemon
      - worker:                    # a daemon with a description, which `status`
          command: bin/sidekiq     #   and WORKSPACE.md show, so a session
          description: background jobs   # knows what it is for
    stop:                          # runs on `chaban down`, after the daemons stop
      - bin/rails tmp:clear
    teardown:                      # runs on `chaban destroy`, before the worktree goes
      - dropdb --if-exists my_app_${WORKSPACE}
    env_allow: [STRIPE_KEY]        # like env_allow above, for this project only
    env:                           # over the global env; also written to the .env
      DATABASE_URL: postgres:///my_app_${WORKSPACE}   # my_app_DEMO-1 in workspace DEMO-1
      REDIS_URL: redis://localhost:6379/${REDIS0}
      PORT: ${PORT0}
    browse_port: ${PORT0}          # the port `chaban browse` opens
    instructions: |                # copied into WORKSPACE.md for the agent
      A booking system for small clinics. Rails 8, PostgreSQL.
      Read my-app/AGENTS.md first.
      Tests: `bin/rspec`.
  shared-lib:
    repo: ~/dev/shared-lib
  acme:                            # uses the template; after filling it in, this is:
    template: rails-client         #   acme:
    params:                        #     repo: ~/dev/clients/acme
      NAME: acme                   #     start:
                                   #       - rails: bin/rails s -p ${PORT0}
                                   #     browse_port: ${PORT0}
```

Runtime tokens are `${WORKSPACE}` (the task id, like `DEMO-1`), `${PROJECT}`
(the project's key, like `my-app`), and the values (`${PORT0}`,
`${REDIS0}`, …). They are filled in inside `env`, `setup`, `start`, `stop`,
`teardown` and `browse_port`. Unknown tokens are left as they are.

### Two YAML traps

- Unquoted `${…}` inside flow style is invalid YAML: `env: {A: ${PORT0}}`
  fails. Use block style, as above, or quote the value.
- A colon followed by a space turns a plain `start:` entry into a map:
  `- echo "run: it"` becomes a daemon named `echo "run`. Quote the whole entry
  when it contains `: `. A colon without a space, as in a URL, is fine.

Template merging, the values math, the `depends` and `path` rules, `.env`
seeding and error positions are in
[`docs/reference.md`](docs/reference.md#configuration-semantics).

---

## Commands

`<ws>` is a workspace: its task id (`DEMO-1`) or its full directory name
(`DEMO-1_try-the-tool`). A task id that matches more than one workspace is an
error that lists them.

| Group | Command | What it does |
|---|---|---|
| **Lifecycle** | `new <task_id> <description> [project…]` | Allocate, create, check projects out. If any step fails, everything is undone. |
| | `checkout <ws> <project…>` | Add projects: worktree, `.env`, `setup`. Safe to repeat. |
| | `destroy [--force] <ws>` | `down`, `teardown`, remove, release. Removes only directories chaban created. |
| **Allocation** | `adopt [dir] [--projects a,b]` | Give an existing directory an allocation and env. Never takes ownership of it. Safe to repeat. Defaults to the current directory. |
| | `release [dir]` | Drop the allocation, touch no files. Defaults to the current workspace. |
| | `gc [-d]` | Release allocations whose directory is gone, clean stale pid files. `-d` also destroys workspaces created by chaban that have at least one project checked out, all merged, clean, with no daemons. |
| **Services** | `up <ws> [target…]` (alias `start`) | Make sure setup is current, run the run-and-waits, start daemons. Safe to repeat. |
| | `down <ws> [target…]` (alias `stop`) | Stop daemons (TERM to the group, KILL after 5s), then run `stop:`. |
| | `restart <ws> [target…]` | `down`, wait until each daemon's main process is gone, then `up`. |
| | `logs <ws> <daemon> [-n N] [-f]` | One daemon's log. `-f` follows stdout and stderr. |
| | `exec <ws> [project] <cmd…>` | Run a command in place of chaban, with the curated environment. |
| | `browse <ws> [project]` | Open `http://localhost:<browse_port>`, after checking something listens there. |
| **Observe** | `ls [-g] [-a]` | Every workspace, one line each. `-g` adds `project@branch` cells (`*` means changed or untracked files). `-a` also lists root directories with no allocation: released workspaces (`adopt` to reuse) and strangers, marked `(unmanaged)`. |
| | `status [ws]` | One workspace in full, read live, plus the `## Status` note its agent keeps. With no argument, the `ls` listing. |
| | `env <ws> [project]` | The resolved environment. |
| | `ports` | Allocated value blocks across workspaces. |
| **Sessions** | `chat <ws> [-R] [args…]` | Start or resume your agent in the workspace. `-R` always starts fresh. Other arguments go to the agent. |
| | `launch <task_id> [<description> [project…]] [-R] [-- args…]` | `new` (or reuse), `checkout` and `chat` in one go. Daemons stay down until you `up` them. |
| | `try <description…> [-R] [-- args…]` | A draft workspace with no projects and a generated `TRY-<n>` id, straight into a session. It still gets its values, so a scratch server has ports. Keep it with `checkout`, or `destroy` it. |
| **Navigate** | `cd <ws> [project]` | Print the directory; the shell wrapper does the `cd`. |
| | `which` | The workspace you're in (exit 3 if none). |
| **Meta** | `doctor` | Health report: config, registry, repos, agent, daemons, stray directories. Reports, never fixes. |
| | `install` / `uninstall` | See [Install](#install). |
| | `completion <shell>` | The completion script for bash, zsh or fish. |

Three commands create workspaces. `new` is for scripts, `launch` is the daily
one, `try` is the scratchpad.

Exit codes: 0 ok, 1 failed, 2 usage, 3 not found, 4 config error. The query
commands take `--json`. Targets for `up`, `down`, `restart` and `logs` are
project names, daemon names, or `project:daemon`: `chaban up DEMO-1 my-app`
runs my-app's run-and-waits and starts all its daemons, and
`chaban down DEMO-1 my-app:worker` stops only the worker.
[More on the commands.](docs/reference.md#command-surface-details)

---

## Environment

Commands chaban runs for you (`setup`, `start:`, `exec`, …) get a curated
environment. Only an allowlist of your variables gets through, plus your
`env:` on top. `PATH` loses per-version bin directories, so version-manager
shims pick each worktree's own `.ruby-version`. To let another variable
through, add it to `env_allow:`.

Agent sessions (`chat`, `launch`, `try`) get your real login environment
instead, without version pins, plus the workspace's values. Daemons need a
reproducible environment; your agent needs your actual setup.

The allowlist, the `PATH` rule and the login-shell caveats are in
[`docs/reference.md`](docs/reference.md#environment-curation--the-shims-model).
Read its *Limits* section once. It will save you a confused hour.

---

## Daemons

A `start:` entry is either a run-and-wait (a plain string: migrations,
preludes) or a daemon (a named entry). Each daemon has its own process group
and its own logs. Its liveness is checked by pid and start time, so a reused
pid doesn't fool it.

Daemons are lazy: nothing starts them until `chaban up`. Give them a
`description:`. `status` shows it to every session, so a session knows what to
start.

`down` sends TERM to each daemon's process group. If the daemon itself is
still alive 5 seconds later, the group gets KILL. With no targets, it stops whatever has a pid file on disk, not what the config lists,
so a daemon you renamed still gets stopped. `stop:` runs after the daemons are
down.

Liveness, the restart details and how logs behave are in
[`docs/reference.md`](docs/reference.md#services--daemons).

---

## Agents

`chaban chat <ws>` starts your coding agent in the workspace. The `agent:`
block in `config.yml` says which agent and how. Any agent you can start from a
shell works. This is the built-in block for Claude Code:

```yaml
agent:
  start: claude
  resume: claude --continue
  can_resume: find ~/.claude/projects/$(pwd | sed 's/[^A-Za-z0-9]/-/g') ~/.claude/projects/$(pwd -P | sed 's/[^A-Za-z0-9]/-/g') -maxdepth 1 -name '*.jsonl' 2>/dev/null | grep -q .
  instructions_file: CLAUDE.md
```

The smallest block is one line. For example, for
[Codex](https://github.com/openai/codex), which reads `AGENTS.md`:

```yaml
agent:
  start: codex
```

Every `chat` then starts a new session, and chaban writes `AGENTS.md` as the
workspace's instructions file. The keys:

- `start` opens a new session. It is the only required key.
- `resume` continues the last session. `can_resume` chooses between the two:
  exit 0 runs `resume`, exit 1 runs `start`. Any other exit, or no answer in
  5 seconds, starts nothing and exits 1. `-R` skips the check. `resume` and
  `can_resume` go together: give both or neither.
- All three run as `$SHELL -lc <command>` in the workspace directory, with the
  session environment. Your extra arguments are added, quoted, to `start` or
  `resume` (whichever runs), never to `can_resume`.
- `instructions_file` is the file chaban writes once at the workspace root,
  with a `## Status` section for the agent to fill in. If your block leaves
  it out, it is `AGENTS.md`. With no `agent:` block at all, it is
  `CLAUDE.md`, to match the `claude` fallback below.

chaban adds no agent flags of its own. `--dangerously-skip-permissions`
reaches Claude Code only if your block says so.

With no `agent:` block, `chat` uses the block above if `claude` is on your
`PATH`, and says so on stderr every time:

```
chaban: no agent: in config.yml, starting claude (found on PATH); add an agent: block to choose
```

With no `claude` either, it prints the block as a sample and exits 4.
`launch` and `try` check this before they create anything.
[The full session rules.](docs/reference.md#sessions)

### The status note

Each workspace's instructions file has a `## Status` section. The agent keeps
it up to date, and `chaban status` shows it. So you, or the next session, can
pick a workspace up again days later without reading old sessions. This works
with any agent: it is a file and a command.

---

## Claude Code extras

Two pieces only help Claude Code. Another agent still gets everything above.
`install` writes both either way; without Claude Code they do nothing.

- **The skill** teaches sessions how to work with chaban: create workspaces
  with `launch` and `try`, start only the daemons the task needs, check work
  against a running server, keep names short, and end a substantial turn with
  a handoff report (done / needs you / watch out).
- **The SessionStart hook** prints the workspace's name and live status into
  every session that opens inside one, including each daemon's description
  and the status note. It ends with the handoff-report rule. The rules live in
  the hook, not in the instructions file: that file is written once and then
  belongs to the agent, while the hook stores nothing, so a change reaches
  every workspace at once.

---

## What chaban deletes

chaban never deletes a directory it didn't create.

- `adopt` manages a directory you made, and never deletes it.
- `release` stops managing a workspace without touching its files.
- `destroy` removes nothing if any stop or teardown fails, so running it
  again picks up where it stopped. It removes worktrees, which discards
  uncommitted changes in them, but it never deletes a branch.
- `gc -d` destroys only what is provably done: created by chaban, at least
  one project checked out, all merged, clean, with no daemons. When in doubt,
  it keeps. A `TRY-` draft with nothing checked out is never collected:
  `destroy` it yourself.

The exact checks, what `--force` does and doesn't do, and what `doctor` looks
at are in [`docs/reference.md`](docs/reference.md#workspace-hygiene).

---

## Why "chaban"

*Chaban* means shepherd. The word came from Persian *chupan* through Turkic
*çoban*, and lives on in Russian and Ukrainian *чабан* and Romanian *cioban*.
A shepherd keeps one flock where every animal has its own pen. chaban does the
same for parallel agent sessions: each workspace gets its own worktree, ports
and daemons. The tool was called claude-workspaces before v2.0.

---

## Development

```sh
go test ./... -count=1      # table tests + testscript command flows
gofmt -l .                  # must print nothing
go vet ./...
CGO_ENABLED=0 go build -ldflags "-X github.com/Phaengris/chaban/v2/internal/cli.version=$(git describe --tags --always)" -o chaban ./cmd/chaban
```

Runtime dependencies: `spf13/cobra`, `goccy/go-yaml`, `golang.org/x/sys` (and
cobra's `pflag`). Tests only: `rogpeppe/go-internal/testscript`.

Layout: `cmd/chaban` is a thin main. `internal/cli` is the cobra tree, one
file per command. `config`, `alloc`, `wsp` and `proc` are the domain;
`envx`, `gitx`, `ui` and `xerr` are leaves. Assets are embedded with
`//go:embed`. Tests are table tests for pure logic plus `testscript` flows
against real processes and real git repos. Each test gets its own
`CHABAN_ROOT_DIR`.

---

## How this was built

chaban was built with coding agents, the way it is meant to be used. I
designed and specified it, and I own every product and architecture decision. The
implementation was done by AI ([Claude Code](https://claude.com/claude-code),
end to end, a clean-room rewrite of an earlier private tool), with an
independent review of each task. The paper trail is checked in: commit
trailers say who typed, the specs say who decided.

- **Spec first:** [`docs/superpowers/specs/`](docs/superpowers/specs/). Every
  decided behavior has a written row.
- **Milestone plans:** [`docs/superpowers/plans/`](docs/superpowers/plans/).
  Binding contracts, named test cases, tables of decided behaviors.
- **Subagent-driven execution:** a fresh AI implementer per task, an
  independent AI review per task, and a whole-branch review before each merge.
- **TDD with mutation-checked tests:** load-bearing assertions are checked to
  fail when the behavior they pin breaks.
- **A human owner in the loop:** design decisions, reviews of the reviews, and
  every release.

After 1.0, development runs the same loop on a smaller scale, driven by daily
use. Most entries in the [CHANGELOG](CHANGELOG.md) trace back to a real moment
at a real terminal.

---

chaban is an independent open-source project (MIT), not affiliated with,
endorsed by or sponsored by Anthropic. Claude and Claude Code are trademarks
of Anthropic.
