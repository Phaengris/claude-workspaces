# claude-workspaces (`workspace`)

An **environment engine**: it provisions N isolated, runnable instances of one
stateful dev stack on a single machine — a git worktree per project, a private
block of ports and other index-derived values, a curated per-instance
environment, and the service lifecycle to go with it — primarily so several
Claude Code sessions can work on several tasks at once without fighting over
port 3000, one database, or one checkout; it is equally usable without them.
Nothing about a workspace is recorded except its allocation: branches, setup
freshness and running daemons are all derived from git, the filesystem and
`/proc` at the moment you ask, so there is no status machine, no "broken
workspace" state, and every command is an idempotent ensure that converges when
re-run. Single static Go binary, no runtime on the host, POSIX only (Linux
first, macOS expected to work).

---

## Install

### 1. Build

```sh
CGO_ENABLED=0 go build -ldflags "-X github.com/Phaengris/claude-workspaces/internal/cli.version=1.0.0" -o workspace ./cmd/workspace
```

`CGO_ENABLED=0` is the point of the exercise (one static file, no libc
coupling). The `-ldflags` value is what `workspace --version` reports; without
it the version reads `dev`, which is a perfectly good answer for a local build.
There is no Makefile and no build tags.

On a **fresh machine** the prerequisites are a Go toolchain (see `go.mod` for
the floor) and `git`; everything the tool ships — the skill, the hook, the shell
wrappers, the starter config — is embedded in the binary, so the two commands
below are the whole install. The daemons you configure have their own
prerequisites, of course.

### 2. Install into `$HOME`

```sh
./workspace install
```

`install` copies **the running binary** (the assets — skill, hook, shell
wrappers, config stub — are embedded in it, so the installed copy never
references your build tree) and writes:

| Path | What |
|---|---|
| `~/.local/bin/workspace` | the binary, mode 0755 |
| `~/.config/fish/functions/workspace.fish` | the fish `cd` **wrapper** (autoloaded) |
| `~/.config/fish/completions/workspace.fish` | generated fish completions (autoloaded) |
| `~/.local/share/workspace/shell/workspace.bash` | the bash/zsh `cd` wrapper — *you* source it |
| `~/.local/share/workspace/completions/workspace.bash` | generated bash completions — *you* source it |
| `~/.local/share/workspace/completions/_workspace` | generated zsh completions — *you* add the dir to `$fpath` |
| `~/.claude/skills/claude-workspaces/SKILL.md` | the Claude Code skill (its own name, so it coexists with the v1 skill) |
| `~/.local/share/workspace/hooks/session-start.sh` | the SessionStart hook script, mode 0755 |
| `~/.local/share/workspace/install-manifest.json` | the list of the paths above — the uninstall contract |
| `<root>/config.yml` | a commented starter config, **only if absent** |
| `<root>/` | the workspaces root, created if missing |

The completion scripts are **generated from the live command tree** at install
time (not shipped as static assets), so they always match the binary you
installed.

`install` deliberately **edits nothing that is yours**: no
`~/.claude/settings.json`, no shell rc file. Instead it prints what to add. The
hook needs this merged into `~/.claude/settings.json` (keep whatever else that
file already has):

```json
{
  "hooks": {
    "SessionStart": [
      { "hooks": [ { "type": "command", "command": "/home/you/.local/share/workspace/hooks/session-start.sh" } ] }
    ]
  }
}
```

The hook is context-only: inside a managed workspace it prints the workspace's
identity and `workspace status` output at session start; outside one it prints
nothing and exits 0. It never mutates anything.

Shell integration — `workspace cd` can only *print* a directory (a child
process cannot chdir its parent shell), so a small function does the actual
`cd` and passes every other subcommand straight through:

```sh
# ~/.bashrc
. "$HOME/.local/share/workspace/shell/workspace.bash"
. "$HOME/.local/share/workspace/completions/workspace.bash"

# ~/.zshrc
. "$HOME/.local/share/workspace/shell/workspace.bash"
fpath=("$HOME/.local/share/workspace/completions" $fpath)
autoload -U compinit && compinit
```

fish needs nothing added: both of its files land in autoload directories.
Make sure `~/.local/bin` is on your `PATH`.

Re-running `install` is idempotent: every tool-owned file is overwritten in
place, `config.yml` is written only when absent (your edits are never
overwritten), and the manifest is rewritten last. Installing *from* the
installed binary is safe — the same-file case is detected and the copy skipped.

### 3. Uninstall

```sh
workspace uninstall
```

Removes **exactly** the paths the manifest lists, then the manifest, and says
what it left behind: the workspaces root (your `config.yml` and your
workspaces), the SessionStart entry you added to `settings.json` (not ours to
remove, since it was not ours to add), and the now-empty tool directories.
Nothing else is ever touched — a manifest entry pointing at `/`, at `$HOME`, at
the workspaces root or at anything inside it is refused rather than executed.
With nothing installed it prints `nothing installed` and exits 0.

---

## Quick start

The root is `~/claude-workspaces` (override with `CLAUDE_WORKSPACES_ROOT_DIR`;
each root is a fully independent universe with its own config and registry).
Describe a project in `<root>/config.yml` — the installed stub explains every
key, and this is enough to follow along with any git repo:

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
workspace ls -g
workspace status DEMO-1
workspace logs DEMO-1 web -n 20
workspace cd DEMO-1
workspace which
workspace exec DEMO-1 demo env
workspace claude DEMO-1
workspace down DEMO-1
cd ~
workspace destroy DEMO-1
workspace gc
```

What just happened: `new` allocated index 0 (so `PORT0..PORT9` = 5000..5009),
created `~/claude-workspaces/DEMO-1_try-the-tool`, wrote `WORKSPACE.md` and a
one-line `CLAUDE.md`, added a `demo` worktree on branch `DEMO-1`, wrote the
project's `.env`, and ran `setup:` once (stamped). `up` ran the bare `start:`
entry to completion and then started the named daemon `web`. `cd` moved the
shell (via the wrapper) and `which` confirmed where it landed. `exec` ran a
command in the **curated** environment. `claude` launched Claude Code in the
workspace dir with `--dangerously-skip-permissions` injected. `down` stopped
the daemon with a confirmed group TERM, `destroy` ran `teardown:`, removed the
worktree, the directory and the allocation, and `gc` found nothing left to
collect.

Three notes on the example. `python3 -u`: a daemon's stdout is a *file*, and
interpreters block-buffer that, so `-u` / `STDOUT.sync = true` / equivalent is
what makes `workspace logs` readable at all. The `logs` line above may still
print nothing — a daemon started a second ago has not necessarily written
anything yet; run it again. And `cd ~` before `destroy`, because destroying the
directory you are standing in leaves your shell in a directory that no longer
exists.

---

## Configuration

One file: `<root>/config.yml`. It is decoded **strictly** — an unknown or
misspelled key is an error with a `line:column` position — and validated at
every load, so every command sees the same verdict. `workspace doctor` prints
the full report. There is no reload step and no cache.

```yaml
# index-derived numbers. Workspace with index i gets
#   NAME0 .. NAME<per_workspace-1>  =  start + i*per_workspace + n
values:
  PORT:
    start: 5000          # must be positive
    per_workspace: 10    # must be >= 1
  REDIS_DB:
    start: 1
    per_workspace: 1     # a block of one is still named REDIS_DB0

# environment for every project's commands, ${…} substituted
env:
  RAILS_ENV: development
  TASK_ID: ${WORKSPACE}

# extra PARENT variables allowed through the curated environment
env_allow: [MY_API_TOKEN, DOCKER_HOST]

# reusable project definitions
templates:
  rails-client:
    params: [NAME]                 # declared load-time parameters
    repo: ~/dev/clients/${NAME}
    base_branch: main
    setup:
      - bundle install
    start:
      - rails: bin/rails s -p ${PORT0}
    browse_port: ${PORT0}

projects:
  my-app:
    repo: ~/dev/my-app             # REQUIRED; ~ expanded
    base_branch: main              # branch off this (default: the repo's HEAD)
    path: my-app                   # subdir inside the workspace (default: the key)
    depends: shared-lib            # a name or a list; orders setup/up, reversed for down
    setup:                         # at checkout; re-run when these lines change
      - bundle install
      - bin/rails db:prepare
    start:                         # what `up` runs, in order
      - bin/rails db:migrate       #   bare string  = run-and-wait
      - rails: bin/rails s -p ${PORT0}   # {name: cmd} = DAEMON
      - worker:                     # {name: {command, description}} = DAEMON,
          command: bin/sidekiq      #   with a description `status`/
          description: background jobs   #   WORKSPACE.md show (${…} substituted)
    stop:                          # optional; runs AFTER this project's daemons stop
      - bin/rails tmp:clear
    teardown:                      # on `destroy`, before the worktree goes
      - dropdb --if-exists my_app_${WORKSPACE}
    env:                           # project env, over the global env
      DATABASE_URL: postgres:///my_app_${WORKSPACE}
      PORT: ${PORT0}
    env_allow: [MY_APP_SECRET]     # also valid per project
    browse_port: ${PORT0}
    instructions: |                # appended verbatim to WORKSPACE.md
      Tests: `bin/rspec`.
  shared-lib:
    repo: ~/dev/shared-lib
  acme:                            # built from the template above
    template: rails-client
    params:
      NAME: acme
```

**Runtime tokens** are substituted when a command runs, in `env`, `setup`,
`start`, `stop`, `teardown` and `browse_port`:

- `${WORKSPACE}` — the **task id** (`DEMO-1`), *not* the directory name
  (`DEMO-1_try-the-tool`). The name is historical; the value is the id.
- `${PROJECT}` — the project name (only when a project is in scope).
- `${PORT0}`, `${PORT1}`, `${REDIS_DB0}`, … — the index-derived values.

Unknown `${…}` tokens pass through untouched, which is what lets load-time
template params and runtime tokens share one syntax. Substitution applies to
**values only**, never to keys, and a substituted value must not itself contain
another token (single-pass, order-dependent, unsupported).

**Templates.** A project's own keys shallow-merge **over** the template's — a
key replaces it wholesale, there is no deep merge. `${PARAM}` is substituted at
**load** time for names declared in `params:` only; runtime tokens pass
through. An unknown template, a missing param, or a param that was never
declared is a validation error.

**Values math.** For value `NAME` with `start: s` and `per_workspace: k`, the
workspace at index `i` gets `NAME0..NAME(k-1)` = `s + i*k + n`. Indices are
assigned lowest-free-first, so a released index is reused — which is exactly
why `release` refuses while daemons are running (see *Workspace hygiene*).
`workspace ports` shows the blocks in use.

**`depends`.** A string or a list of project names. It gives a topological
order over the workspace's *checked-out* projects (edges to projects that are
not checked out here are ignored): `checkout`/`up` follow it, `down` and
`teardown` reverse it. A cycle, or a dependency on an unconfigured project, is
a validation error.

**`path`.** Where the worktree lands inside the workspace; defaults to the
project key. It must be relative, must contain no `..` component, and must not
resolve to the workspace dir itself — `destroy` force-removes that directory,
so an escaping value is rejected at load time rather than trusted later.

**`.env` seeding.** At checkout the source repo's own `.env` is read as
defaults and the workspace's resolved env is merged on top (the workspace
always wins); the result is written to the worktree's `.env`, sorted `K=V`,
mode 0600. Blank lines and `#` comments in the source are skipped; a line
without `=` is skipped. A line spelled `export FOO=bar` parses as the key
`export FOO` — it round-trips into the written file unchanged, but it does **not**
override a workspace `FOO`, so write plain `FOO=bar` in repo `.env` files you
want to layer under this. **Add `.env` to each repo's `.gitignore`**: checkout
writes it *into* the worktree, so a repo that tracks `.env` reads dirty in every
workspace — to `ls -g`'s `*` and to `gc --destroy-dirs`'s clean check alike.

**Two YAML caveats**, both worth knowing before they bite:

- *Flow style and `${…}`*: the YAML spec forbids `{`/`}` in plain (unquoted)
  scalars inside flow collections, so `env: {A: ${PORT0}}` and
  `setup: [echo ${PORT0}]` are **invalid YAML**. Use block style (as every
  example here does) or quote the value: `env: {A: "${PORT0}"}`. Ruby's Psych
  tolerated this; the Go parser correctly rejects it.
- *A colon inside a bare `start:` entry*: `- echo "run: it"` is a single-key
  **map** to YAML, so it becomes a daemon named `echo "run` — not a
  run-and-wait command. Quote the whole entry (`- 'echo "run: it"'`) whenever it
  contains `: `.

**Error positions.** Strict-decode errors quote the position of the bytes that
were decoded. A config with **no templates** is decoded from your file, so the
positions point at `config.yml`. A config that **uses templates** must be
expanded and re-marshaled first, so its positions refer to that regenerated
(key-sorted, re-laid-out) document — the error message says so explicitly when
that applies.

**Ordering** of user-visible output (env files, project lists, workspace
listings) is alphabetical by contract, never insertion order.

---

## Commands

Grouped as the design document groups them. `<ws>` is a **workspace
identifier**: the full directory name or the task id. An ambiguous task id is a
plain error listing the candidates.

| Group | Command | What it does |
|---|---|---|
| **Lifecycle** | `new <task_id> <description> [project…]` | Allocate, create the dir, write `WORKSPACE.md` + `CLAUDE.md`, check the listed projects out. **Transactional**: any failure undoes everything this invocation made. |
| | `checkout <ws> <project> [project…]` | Add projects: worktree, `.env`, stamped `setup`, refresh `WORKSPACE.md`. Idempotent. |
| | `destroy [--force] <ws>` | `down` → `teardown` → remove worktrees + dir → release the allocation. Only ever removes dirs the tool created. |
| **Allocation** | `adopt [dir] [--projects a,b]` | Give an existing directory an allocation, values and per-project `.env`s. Detects projects from git, or takes the list. Never creates, never claims ownership. Idempotent. |
| | `release [dir]` | Drop the allocation, touch no files. Defaults to the workspace containing the cwd. |
| | `gc [-d\|--destroy-dirs]` | Release vanished allocations, reap stale pid files; with `-d` also destroy fully merged, clean, daemonless, tool-created workspaces. |
| **Services** | `up <ws> [target…]` (alias `start`) | Ensure setup is current, run the run-and-waits, start daemons not already running, in dependency order. Idempotent. |
| | `down <ws> [target…]` (alias `stop`) | Stop daemons (group TERM → poll ≤5s → KILL), report which signal was needed, then run `stop:`. |
| | `restart <ws> [target…]` | `down` with confirmed death, then `up`, over the same target set. |
| | `logs <ws> <daemon> [-n N] [-f]` | Print one daemon's stdout log (`-n`, default 50; `-n 0` for none) and optionally follow it (`-f`, which also streams stderr). |
| | `exec <ws> [project] <cmd> [args…]` | **Replace** this process (`execve`) with the command, in the workspace or a project worktree, under the curated environment. |
| | `browse <ws> [project]` | Open `http://localhost:<browse_port>`; with no `xdg-open` on `PATH`, print the URL instead (exit 0 — the SSH-friendly path). |
| **Observe** | `ls [-g]` | Every workspace, one line each; `-g` appends `project@branch` per checked-out project, `*` when dirty (git runs concurrently, bounded). |
| | `status [ws]` | One workspace in full, every field derived live; with no argument, the `ls` listing. |
| | `env <ws> [project]` | The resolved environment as sorted `K=V`. |
| | `ports` | Allocated value blocks across workspaces. |
| **Sessions** | `claude <ws> [-S] [-R] [claude args…]` | Claude Code in the workspace dir, with flag injection. |
| | `launch <task_id> [<description> [project…]] [-S] [-R] [-- claude args…]` | `new`-or-reuse + `checkout` + `up` + `claude`, one shot. |
| **Navigate** | `cd <ws> [project]` | Print the directory; the installed shell wrapper performs the `cd`. |
| | `which` | The workspace containing the cwd (exit 3 when there is none — the scriptable "am I in a workspace?"). |
| **Meta** | `doctor` | Config report + registry + allocations + missing repos + daemon health. Reports, never fixes. |
| | `install` / `uninstall` | See *Install*. |
| | `completion <shell>` | The generated completion script (`bash`, `zsh`, `fish`; `powershell` comes free from the framework and is untested here). |

**Global flags.** `--version`/`-v`, `--help`/`-h`, and `--json`. `--json` is
**scoped to the query commands** — `ls`, `status`, `env`, `ports`, `which`,
`doctor`. Every other command accepts and ignores it (so a caller that sets it
globally never breaks), because there is no query result to serialize;
`cd`/`browse` print a single path or URL, which already *is* the machine-readable
form, and a log is bytes some process wrote.

**Exit codes** (v1 exited 1 for everything; scripts and the skill can now
distinguish):

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | command/operation failure |
| 2 | usage (bad flag, wrong arg count, unknown command) |
| 3 | workspace or project not found |
| 4 | config error |

A `claude`/`launch` session propagates **Claude's own exit code verbatim**, so a
3 from there is Claude's 3, not "not found".

**Service targets** (`up`/`down`/`restart`/`logs`) share one grammar: a target
is a project name or a daemon name, resolved against the workspace's
checked-out projects first, then their daemons. A daemon name defined by more
than one project is ambiguous — qualify it as `project:daemon`. Addressing a
single daemon still runs its project's ensure-chain first. No target means the
whole workspace.

**Completions** cover workspace identifiers, project names (templated projects
included), and daemon targets for the command and position you are at. A
completer never reports a failure: a broken config or registry collapses to "no
suggestions" rather than printing an error at your prompt or falling back to
file names in a workspace slot. The two session commands (`claude`, `launch`)
disable flag parsing so everything can reach Claude, which also means the shell
can only be helped with their first argument.

---

## Environment curation — the shims model

There is no per-command version-manager wrapper. Three mechanisms, applied to
**every** command the tool runs for you — `setup`, `start`, `stop`, `teardown`,
`exec`:

1. **An allowlist.** A spawned command receives only these parent variables,
   by **exact name** (not globs — the list cannot grow by accident):

   ```
   HOME USER LOGNAME SHELL TERM TERM_PROGRAM
   LANG LANGUAGE LC_ALL LC_CTYPE LC_MESSAGES LC_COLLATE LC_NUMERIC LC_TIME
   TZ DISPLAY WAYLAND_DISPLAY XAUTHORITY SSH_AUTH_SOCK SSH_AGENT_PID
   GPG_AGENT_INFO GNUPGHOME XDG_RUNTIME_DIR XDG_CONFIG_HOME XDG_DATA_HOME
   XDG_CACHE_HOME XDG_STATE_HOME DBUS_SESSION_BUS_ADDRESS
   ```

   plus the resolved workspace/project `env` on top. Version-manager **pin
   variables** are dropped by *prefix* — `RBENV_ PYENV_ NODENV_ PLENV_ GOENV_
   RUBYENV_ ASDF_ MISE_ __MISE_` — so an activated shell cannot pin a
   workspace's commands to the version that happened to be active when you
   launched them. An `env_allow` entry that names a pin variable **exactly**
   outranks the prefix drop (explicit intent wins).

2. **A sanitized `PATH`.** `PATH` survives, minus the segments that are
   concrete per-version install bins — a segment containing `/versions/` or
   `/installs/` and ending in `/bin`. Version-manager **shims** therefore stay
   reachable and resolve each worktree's own `.ruby-version` /
   `.tool-versions` by cwd. `PATH` is **always** sanitized, even if you name it
   in `env_allow`; the `env:` blocks are the raw-override channel.

3. **Startup self-sanitize.** The same treatment is applied to the tool's own
   process environment before anything is spawned, so even inherit-spawns start
   clean.

`exec` closes v1's documented env-poisoning bug by construction: the curated
slice is the *complete* child environment, and `exec` has no other spawn path.

**Two tiers, deliberately.** Commands the tool runs *for you* get the curated
environment above. A Claude **session** (`claude`, `launch`) gets the full
**inherited** environment instead — already self-sanitized at startup, so
version-manager pins are gone — overlaid with the workspace's resolved
**global** `env` and the runtime variables themselves (`WORKSPACE`, `PORT0`, …)
exported as real variables. Claude is the operator's tool and needs the real
login environment. Note the consequence: a **project's** `env` entry reaches
`exec`, `setup` and daemons, but **not** the session — `workspace exec T-1 app env`
and the session's environment legitimately differ.

### Limits — read these once

This is a compromise, as any answer here is. The behavior is documented rather
than mysterious:

- **A parent variable that is not on the allowlist silently does not reach
  spawned commands.** No warning is possible — the tool cannot know which of
  your thousand variables mattered. The fix is `env_allow` (global or per
  project) or an explicit `env:` entry.
- **The `PATH` heuristic recognizes common version-manager layouts**
  (`…/versions/<v>/bin`, `…/installs/<tool>/<v>/bin`). An unrecognized layout
  degrades to over-keeping a segment, which reproduces pin-to-launch-time
  behavior — never anything worse.
- **Commands run under `$SHELL -lc "<command>"`** (fallback `/bin/sh`), so your
  **login shell's own init runs first** and can reintroduce environment the
  allowlist just removed. That is outside the tool's control; if it matters,
  keep environment mutation out of your login files (or out of the
  non-interactive branch of them).
- **`$SHELL` is read from the tool's own process environment**, not from the
  curated one, so setting `SHELL` in `env:` changes what the *child* sees but
  not the interpreter. The practical consequence: your config's command strings
  are interpreted by **your login shell** — if that is fish, `FOO=bar cmd`,
  `export FOO=bar` and `$(…)` are not valid, whatever your CI thinks. Write
  one-liners that work in *your* shell, or spell it out: `sh -c '…'`.
- A failed run-and-wait reports **the first non-empty stderr line**. Under a
  *login* shell that can be a line your `/etc/profile` printed rather than the
  command's own complaint; `workspace logs` and the command's own output are
  the fallback when a message looks unrelated.

---

## Services & daemons

`start:` entries are two kinds of thing, and one custom YAML unmarshaler owns
the distinction:

- a **bare string** is a *run-and-wait*: `up` runs it to completion, captures
  its output, and reports only on failure. Nothing is logged and nothing is
  tracked. Use it for migrations and other preludes.
- a **single-key map** (`name: command`) is a **daemon**: its own process
  group, stdout to `.workspace/logs/<project:daemon>.log` and stderr to
  `<project:daemon>.err.log` (both truncated at every start), and a pid file
  `.workspace/pids/<project:daemon>` holding `<pid> <starttime>`. The value
  can also be a nested `{command, description}` map — same daemon, plus an
  optional `description:` that `status` and `WORKSPACE.md` show (with `${…}`
  substituted) so a session knows what the daemon is for before starting it.

Run-and-waits belong to the **project**, not to any one daemon: they run when
the whole project is targeted, and are skipped when you address a single daemon
— exactly as `stop:` is.

**Liveness** is `pid` **and** `starttime` (field 22 of `/proc/<pid>/stat`), which
makes it pid-reuse-proof: a recycled pid reads as *not running* rather than as
someone else's process. On systems without `/proc` the starttime records as `0`
and the check degrades to pid-only. A pid file that is missing, corrupt or names
a dead process is **not running** — every consumer treats it identically, and
`gc` reaps it.

**`up`** ensures each project (worktree, `.env`, stamped `setup`) even when you
addressed a single daemon, then runs the prelude, then starts what is not
already running. `started` means *spawned and recorded*, not *healthy* — a
daemon that exits immediately says so in its `.err.log` and reads as not
running from then on. Setup is re-run when the *rendered* `setup:` lines change
(the stamp is a SHA-256 of them).

**`down`** walks the dependency order backwards, and within a project its
daemons in reverse listed order. Each running daemon gets SIGTERM to its
**process group**, then ≤5s of polling, then SIGKILL, and the line says which
sufficed: `stopped app:rails (TERM)` / `(KILL)`. Already-stopped daemons print
`already stopped` and are not signaled. The pid file is removed only on
confirmed death, so a failed stop leaves the record for a retry.

> **`stopped (TERM)` promises the recorded *leader* is dead — not every group
> member.** A member that ignores TERM under a leader that obeys it can linger.
> Polling for group emptiness is deliberately absent: zombies would hang it.

> **`stop:` runs AFTER this project's daemons stop** — v1 ran it *before*
> killing them. A `stop:` command that talks to a running daemon (a graceful
> drain, say) therefore behaves differently here; drain logic belongs in the
> daemon's own TERM handler. There is no pre-stop hook in v1.0. When the
> project is not checked out, `stop:` is skipped (stopping must not create
> worktrees) — loudly, if you configured any, because those commands may manage
> state outside the worktree.

**With no explicit target**, `down` (and `destroy`'s stop phase, and
`restart`'s down half) takes its inventory from the **pids directory**, not
from the config: a pid file is named after the key `up` wrote it under, so a
daemon you renamed, dropped from `start:`, or whose project you deleted from
the config still holds this workspace's ports while being invisible to any
config-driven walk. Those extra keys are stopped after everything
config-resolved, in alphabetical order, and only when actually **live** (a dead
or corrupt stray is `gc`'s garbage, not `down`'s work, and is passed over in
silence). An **explicitly named** target is still resolved through the config
alone — a name you typed must mean what config says it means.

**`restart`** is `down` then `up` over the same targets, and it converges: a
target that was already stopped just starts. The two halves are **not
symmetric** with no explicit target, and cannot be — the down half stops every
live recorded key, config-known or not, while the up half can only start what
`start:` defines. So restarting a workspace whose config no longer defines a
running daemon **stops it and does not bring it back**; restore the config
entry (or use `up`). If the pids directory cannot even be listed, the up half
is refused outright: starting daemons beside processes we cannot see would
double whatever holds these ports.

**`logs`** prints the `.log` only (`-n`, default 50). With `-f` it follows
**both** streams, raw and interleaved, unlabeled — stderr is where a dying
daemon explains itself — printing only what arrives after the follow starts. A
daemon that writes exclusively to stderr (`python -m http.server`, for
instance) therefore shows an empty tail; when that happens and the `.err.log`
is non-empty, one note points at it: `(no stdout output; stderr has output — try -f)`.
A daemon that has never run is a note and exit 0, not a failure.

**`browse`** substitutes `browse_port` for this workspace and opens
`http://localhost:<port>` with `xdg-open`, detached. With no `xdg-open` on
`PATH` it prints the URL and exits 0 — on a remote box, printing *is* the
feature. With one project checked out it needs no argument; with several it
asks you to pick.

Daemons get their own **process group** (that is what makes group stop
possible) but deliberately **not their own session** — no `setsid`. They are
released by the CLI and reparented to init, with stdio already redirected to
their log files.

---

## Sessions: `claude` and `launch`

```sh
workspace claude DEMO-1                    # session in the workspace dir
workspace claude DEMO-1 -S                 # ... without --dangerously-skip-permissions
workspace claude DEMO-1 -R                 # ... without --continue
workspace claude DEMO-1 --model opus       # anything else goes to claude
workspace launch DEMO-1 "try the tool" demo -- --model opus
```

Both **disable flag parsing**: every flag except the tool's own two belongs to
Claude, and the injection rules are:

- `--dangerously-skip-permissions` is injected **unless** you passed `-S` /
  `--claude-no-skip-permissions`, or took your own permission stance
  (`--permission-mode[=…]`, or the flag itself).
- `--continue` is injected **only when a conversation already exists for this
  workspace directory**, and not when you passed `-R` /
  `--claude-no-resume`, not in print mode (`-p`/`--print`), and not when you
  passed your own resume flag (`-c`, `--continue`, `-r`, `--resume[=…]`,
  `--from-pr[=…]`).

Everything after a literal `--` is Claude's, verbatim — including a later `--`,
and including strings spelled exactly like `-S`/`-R`. The tool's flags are only
recognized *before* the first `--`. The **workspace identifier must come first**:
with flag parsing off, `workspace claude --json DEMO-1` would otherwise resolve
a workspace literally named `--json`, so a leading flag-looking token is a usage
error (exit 2) that says so.

Three sharp edges worth knowing:

- **The history probe.** Whether a conversation exists is decided by looking for
  `~/.claude/projects/<encoded dir>/*.jsonl`, where the encoding maps **every
  non-alphanumeric byte to `-`** (verified empirically against real Claude Code
  state, not guessed: `/home/cat/claude-workspaces/PATADM_patternima-admin-panel`
  → `-home-cat-claude-workspaces-PATADM-patternima-admin-panel`, underscore
  included). Both the directory as recorded and its symlink-resolved form are
  probed, so a workspaces root reached through a symlink still finds its
  history. Every failure to look is "no history", and the safe failure direction
  is a **fresh session instead of `--continue`** — never the reverse.
- **Bundled short flags are invisible to the injection detector.** It compares
  whole tokens, so `-cp` is *not* seen as `-c` + `-p`: `--continue` would be
  injected alongside your `-c`. Write short flags separately (`-c -p`) when you
  care.
- **`exec`'s project sniff, and the `--` rule.** In
  `workspace exec <ws> [project] <cmd…>` the argument right after the workspace
  is the **project** if (and only if) it names a configured project; otherwise it
  *is* the command. To run a command that happens to be named like a project,
  put `--` in that one slot (`workspace exec T-1 -- app`) or give a path
  (`./app`). `--` is the sniff suppressor **only** in that position: a later
  one belongs to your command and is passed through untouched, so
  `workspace exec T-1 app git checkout -- README` still restores a file.

`launch` composes the daily entry sequence — create-or-reuse, check out, bring
the whole workspace up, then hand over the terminal — by calling the same work
functions the individual commands use, so it cannot drift from them. Any phase
that fails stops the sequence, so a session never opens onto a half-built
environment. Reuse **ignores a supplied description silently** (the
`using existing workspace <name>` line is the notice), and positional 2 is
*always* the description slot on both paths — so when it happens to name a
configured project, a note says what became of it, because you almost certainly
meant `launch <id> <desc> <project…>`.

The installed **skill** (`~/.claude/skills/claude-workspaces/SKILL.md`) is
the session-facing interface — "work on FIZZY-123" turns into
`new`/`checkout`/`up` and a `cd`. The **SessionStart hook** adds identity and
status as context and never mutates. `WORKSPACE.md` holds the task, the
allocated values, the per-project `instructions`, and each project's daemons
and what they're for; `CLAUDE.md` is created
**once** with a single reference line to it and never rewritten, so notes
accumulated there survive every regeneration.

---

## Workspace hygiene

Four commands, and one rule that runs through all of them: **the tool never
deletes a directory it did not create.**

- **`adopt [dir]`** gives an existing tree an allocation — an index, values, a
  `.env` per project, `WORKSPACE.md`, `CLAUDE.md` if absent — and records it as
  adopted. It creates no worktrees, clones nothing, moves nothing, and runs no
  setup. The task id is the directory's **base name, verbatim** (it must be a
  valid id — rename the directory rather than have the name on disk and the
  name in the tool disagree). Projects are detected from git worktree metadata
  unless `--projects` replaces the detected set outright. Adopting the
  workspaces root itself is refused.
- **`release [dir]`** drops the allocation and touches no files — not even for
  a tool-created workspace. It is the "this is mine now, stop managing it"
  escape hatch, and it is idempotent (a miss is exit 0). **It refuses while any
  daemon is running**: the allocation is what makes the workspace addressable
  and its index exclusive, so releasing it under a live daemon would strand the
  process twice over — `down <name>` could no longer resolve it, and the freed
  index would be handed to the next `new`, whose daemons would collide on the
  same ports. Stop them first.
- **`destroy [--force] <ws>`** is `down` (the whole workspace, pids directory
  included) → `teardown` per checked-out project in reverse dependency order →
  remove worktrees → remove the dir → release. **A stop or teardown failure
  aborts everything and removes nothing**, so re-running converges. A project's
  first failing teardown command stops that project's teardown; the remaining
  projects still run theirs (convergence over strict ordering — teardown
  commands are expected idempotent). Worktree removal discards the working copy
  but **never the branch**: a branch is your work. An **adopted** workspace gets
  `down` + `teardown` + `release` only, with its files left exactly where they
  are. `--force` is narrow on purpose: it downgrades **worktree-removal**
  failures to warnings (then prunes best-effort), which is the escape hatch for
  a source repo that moved or was deleted. It does not skip safety — a corrupt
  registry entry pointing outside the root is still refused, and live daemons
  and failing teardowns still abort. `--force` is also how you finish the one
  documented exception to `new`'s "leaves nothing behind": if `new`'s undo cannot
  remove a worktree it stops there deliberately, leaving the workspace dir and
  its allocation in place — addressable state beats an orphan git still has
  bookkeeping for.
- **`gc [-d]`** is the batch sweep, in up to three passes: release every
  allocation whose **dir has vanished**; **reap stale pid files** — every record
  in the pids *directory*, dead or corrupt alike, live ones untouchable; and
  with `-d`, destroy what is provably collectable. One workspace's failure
  never abandons the rest, and a batch failure is exit 1 (per-workspace codes
  are meaningless once several failures are one error). Note that an
  **unreadable** dir is not a vanished dir, and an unreadable pids directory is
  a loud per-workspace error, never an assumed quiet.

**What survives `gc --destroy-dirs`.** A workspace is destroyed only on
evidence — every gate must say yes:

| Gate | Destroyed when | Survives when |
|---|---|---|
| Ownership | tool-created | **adopted** (never deleted, silently) |
| Content | ≥ 1 project checked out | **nothing checked out** — an empty workspace has no branches, hence no merge evidence at all. A half-destroyed workspace reads as empty here too: finish it with `destroy` (or `destroy --force`). |
| Daemons | nothing in the pids **directory** names a live process | any live record, config-known or not; an unreadable pids dir is an error and a skip |
| Merged | every checked-out project's `<task_id>` branch is fully merged into its base | any unanswerable merge question — missing branch, missing base, moved repo, detached HEAD — reads as *not merged* |
| Clean | no checked-out worktree is dirty (modified or untracked) | dirty, and **loudly**: `skipped <name> (uncommitted changes)`. `destroy` keeps the power to discard uncommitted work; a batch sweep does not. |

The base for the merge check is the project's `base_branch`, or — when that is
empty — the **source repo's own HEAD branch at gc time**, mirroring what
`checkout` branched from. (The branch is compared refs/heads-qualified while the
base is taken unqualified: a documented asymmetry.) The clean check is the same
predicate `ls -g` renders — which is why an untracked `.env` in a repo that does
not gitignore it makes `gc -d` collect nothing, tool-wide. One definition of
dirty, and the fix lives in the repo.

**A live Claude session does not protect a workspace.** The daemon gate reads
pid files, and a session (or a plain shell) sitting in the directory writes
none — so a merged, clean, daemonless workspace can be collected out from under
one. A session pid marker is post-1.0 work; until then, `gc -d` is a deliberate
act, and an unmerged or dirty workspace is safe by the gates above.

**`doctor`** reports and never fixes (`gc` and `down` fix). It prints the config
verdict, the configured projects and value blocks, then: stale allocations
(dir gone), allocations **outside the root** — informational for an adopted
workspace, a finding for a tool-created one, which is a corrupt or hand-edited
registry and something `destroy` will refuse — configured projects whose `repo`
does not exist, and per-workspace daemon health from the pids directory (live
counts, stale records, keys the config cannot name). It ends with
`doctor: N finding(s)` or `doctor: no findings` and **exits 0 regardless**:
findings are observations, not failures. Only an invalid config (exit 4) and an
unreadable registry (exit 1) are errors. `--json` gives `findings` and
`informational` arrays with a stable `kind` per entry.

---

## v1 divergences

Consolidated, for anyone coming from the Ruby tool. This is a **new tool** with
a new command surface and new on-disk formats, not a port: live v1 workspaces
are not migrated (they finish on Ruby), and the two coexist — the skill here is
installed under its own name, `claude-workspaces`.

- **Dropped commands**: `archive`/`unarchive` (a workspace with daemons down and
  its allocation released *is* archived; re-provisioning is `adopt`), `resolve`
  (there is no broken state to resolve), `setup` as a command (it is an
  ensure-step inside `checkout` and `up`), `title`, `welcome` (folded into
  `--help`), `prune` (→ `gc`). Also gone: `command_runner` — environment
  curation replaces it.
- **Exit codes** are meaningful: 0/1/2/3/4 instead of 1 for everything.
- **`stop:` runs after** the project's daemons stop, not before.
- **`exec` runs under the curated environment.** v1's `exec` leaked the parent
  environment; here there is no inherit path to leak through.
- **State**: one registry, `<root>/.allocations.json` (JSON, per-root, flock'd,
  atomically written), and nothing else. Everything else is derived on demand:
  no status machine, no "broken workspace", no recorded project list.
- **`WORKSPACE.md` + `CLAUDE.md`-once**: regeneration only ever rewrites
  `WORKSPACE.md`, so agent notes in `CLAUDE.md` survive (v1 lost them).
- **YAML is stricter**: unknown keys are errors with positions, and unquoted
  `${…}` inside flow collections is rejected — Ruby's Psych tolerated it. Error
  positions refer to the regenerated document when templates are in play.
- **A pre-existing non-empty directory at a project's destination now errors
  loudly** (v1 succeeded silently). An **empty** directory is adopted by
  `git worktree add`, as git does.
- **`stopped (TERM/KILL)` promises leader death only** (see *Services*).
- **A workspace directory reached through a symlink** reads as "not checked
  out", and `which` compares paths **as written** (no `EvalSymlinks`) — the
  registry records a spelling, and the shell wrapper `cd`s to the spelling the
  tool printed, so the two agree by construction. Reaching the same directory by
  another route is honestly reported as "not inside a workspace". (The Claude
  history probe is the one place that tries both spellings, because Claude Code
  records whichever it ran in.)
- **Non-goals** carried from the design: no Docker runtime, no Windows, and no
  profiles (named `start:` subsets, e.g. `test` = databases only) in 1.0.

---

## Development

```sh
go test ./... -count=1      # table tests + testscript command flows
gofmt -l .                  # must print nothing
go vet ./...
CGO_ENABLED=0 go build -ldflags "-X github.com/Phaengris/claude-workspaces/internal/cli.version=$(git describe --tags --always)" -o workspace ./cmd/workspace
```

Runtime dependencies are deliberately three, four modules with `pflag` (which is
cobra's own): `spf13/cobra` for the CLI and the completion generators,
`goccy/go-yaml` for YAML with positions in its errors, `golang.org/x/sys` for
`flock`. Test-only: `rogpeppe/go-internal/testscript`. `go version -m` on a
built binary is the check.

Layout: `cmd/workspace` is a thin main (error → exit code); `internal/cli` is
the cobra tree, one file per command; the domain packages are `config` (strict
load, templates, validation), `alloc` (the registry, locking, index assignment,
values math), `wsp` (workspace operations, derived status, stamps, the writers,
the curated spawn env), `proc` (run-and-waits, daemons, liveness, kill
escalation); the leaves depend on nothing internal: `envx` (allowlist, PATH
sanitizer), `gitx` (argv-form git only, never a shell), `ui` (human/JSON
rendering), `xerr` (error kinds → exit codes). Assets under `assets/` are
embedded with `//go:embed`. Config is loaded once and passed down explicitly —
no package-level singletons.

Tests are two layers: table tests for the pure logic (template expansion,
values math, start-entry unmarshal, allowlist and PATH sanitizer, claude-flag
extraction, index gap-filling, topo sort) and `testscript` scripts under
`internal/*/testdata` for command flows, each in a tmpdir root isolated by
`CLAUDE_WORKSPACES_ROOT_DIR`. Install/uninstall tests only ever run against a
fake `$HOME`.
