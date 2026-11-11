# Reference

The contracts and reasoning behind the [README](../README.md)'s claims. The
README tells you what the tool does; this file tells you exactly how, and why
it was decided that way. Nothing here is needed to start — it is where to look
when a detail matters.

---

## Configuration semantics

**Runtime tokens** are substituted when a command runs, in `env`, `setup`,
`start`, `stop`, `teardown` and `browse_port`:

- `${WORKSPACE}` — the **task id** (`DEMO-1`), *not* the directory name
  (`DEMO-1_try-the-tool`).
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

**Values math.** For value `NAME` with `start: s` (must be positive) and
`per_workspace: k` (must be >= 1), the workspace at index `i` gets
`NAME0..NAME(k-1)` = `s + i*k + n`. A block of one is still numbered:
`per_workspace: 1` yields `NAME0`. Indices are
assigned lowest-free-first, so a released index is reused — which is exactly
why `release` refuses while daemons are running (see *Workspace hygiene*).
`chaban ports` shows the blocks in use.

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

**Checkout collisions.** A pre-existing **non-empty** directory at a project's
destination errors loudly; an **empty** directory is adopted by
`git worktree add`, as git does.

**Error positions.** Strict-decode errors quote the position of the bytes that
were decoded. A config with **no templates** is decoded from your file, so the
positions point at `config.yml`. A config that **uses templates** must be
expanded and re-marshaled first, so its positions refer to that regenerated
(key-sorted, re-laid-out) document — the error message says so explicitly when
that applies.

**Ordering** of user-visible output (env files, project lists, workspace
listings) is alphabetical by contract, never insertion order.

---

## Command-surface details

**Global flags.** `--version`/`-v`, `--help`/`-h`, and `--json`. `--json` is
**scoped to the query commands** — `ls`, `status`, `env`, `ports`, `which`,
`doctor`. Every other command accepts and ignores it (so a caller that sets it
globally never breaks), because there is no query result to serialize;
`cd`/`browse` print a single path or URL, which already *is* the machine-readable
form, and a log is bytes some process wrote.

**Service targets** (`up`/`down`/`restart`/`logs`) share one grammar: a target
is a project name or a daemon name, resolved against the workspace's
checked-out projects first, then their daemons. A daemon name defined by more
than one project is ambiguous — qualify it as `project:daemon`. Addressing a
single daemon still runs its project's ensure-chain first. No target means the
whole workspace.

**Completions** cover workspace identifiers, project names (templated projects
included), and daemon targets for the command and position you are at,
offered case-insensitively, and for a workspace also when what you typed is
*inside* the name (`cd 430<TAB>` reaches `FIZZY-430_…`). What you see is up to
the shell. fish uses both, and shows inside-matches only when no name starts
with your text. bash's completion script keeps only exact-case prefix matches.
zsh does the same by default; a `matcher-list` style turns on case-insensitive
and substring matching there. A completer never reports a failure: a broken
config or registry collapses to "no suggestions" rather than printing an error
at your prompt or falling back to file names in a workspace slot. The
session commands (`chat`, `launch`, `try`) disable flag parsing so everything
can reach the agent, so completion helps only with `chat`'s workspace and
`launch`'s task id and project names, never with the agent's own flags.
(`completion powershell` comes free from the CLI framework and is
untested here.) `ls -g`'s git stats run concurrently, bounded.

**Exit codes.** Once a `chat`/`launch`/`try` session starts, chaban exits
with **the code of the shell that ran the agent, verbatim**, so a 3 from
there is the agent's 3, not "not found". Normally that is the agent's own
code. A signal death depends on the shell and on who got the signal. When
the shell replaced itself with the agent (bash does, for a simple command),
chaban sees the signal and exits 1. When the shell is still there (fish, or
bash running a compound command like `a && b`) and the signal hit the agent
alone, the shell can exit 128+n (143 for a SIGTERM sent to the agent), and
chaban passes that on. A ^C reaches the shell too. If the agent survives it
(most interactive agents do), chaban exits with the agent's own code, under
fish as well; exit 1 is for an agent that dies from the signal. A configured agent whose program is missing is the
shell's 127. A failed `can_resume` check is exit 1, before anything starts.

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

`exec`'s curated slice is the *complete* child environment — there is no
inherit path through which the parent environment could leak.

**Two tiers, deliberately.** Commands the tool runs *for you* get the curated
environment above. An agent **session** (`chat`, `launch`, `try`), and its
`can_resume` check, get the full **inherited** environment instead — already
self-sanitized at startup, so version-manager pins are gone — overlaid with
the workspace's resolved **global** `env` and the runtime variables
themselves (`WORKSPACE`, `PORT0`, …) exported as real variables. The agent is
the operator's tool and needs the real login environment. Note the
consequence: a **project's** `env` entry reaches `exec`, `setup` and daemons,
but **not** the session — `chaban exec T-1 app env`
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
- **Commands run under `$SHELL -lc "<command>"`** (fallback `/bin/sh`), the
  `agent:` commands included, so your
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
- **Argument passthrough is guaranteed for sh, bash, zsh and fish only.**
  `chat`'s extra arguments are quoted into the agent's command line in a
  form those four read back verbatim. Under another `$SHELL` (tcsh,
  nushell, …) an argument may be split or mangled.
- A failed run-and-wait reports **the first non-empty stderr line**. Under a
  *login* shell that can be a line your `/etc/profile` printed rather than the
  command's own complaint; `chaban logs` and the command's own output are
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
(the stamp is a SHA-256 of them). `new`, `checkout` and `up` report the
ensure chain as it actually runs: each checkout and setup command actually
executed prints its own `label… ok (0.3s)` (or `failed`) line, with a real
duration — a project that was already checked out with current setup stays
silent.

**`down`** walks the dependency order backwards, and within a project its
daemons in reverse listed order. Each running daemon gets SIGTERM to its
**process group**, then ≤5s of polling, then SIGKILL, and the line says which
sufficed: `stopped app:rails (TERM)` / `(KILL)`. Already-stopped daemons print
`already stopped` and are not signaled. The pid file is removed only on
confirmed death, so a failed stop leaves the record for a retry.

> **`stopped (TERM)` promises the recorded *leader* is dead — not every group
> member.** A member that ignores TERM under a leader that obeys it can linger.
> Polling for group emptiness is deliberately absent: zombies would hang it.

> **`stop:` runs AFTER this project's daemons stop.** A `stop:` command that
> talks to a running daemon (a graceful drain, say) will not find one; drain
> logic belongs in the daemon's own TERM handler — there is no pre-stop hook.
> When the project is not checked out, `stop:` is skipped (stopping must not
> create worktrees) — loudly, if you configured any, because those commands
> may manage state outside the worktree.

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

**`logs`** prints the `.log` only (`-n`, default 50; `-n 0` for none). With `-f` it follows
**both** streams, raw and interleaved, unlabeled — stderr is where a dying
daemon explains itself — printing only what arrives after the follow starts. A
daemon that writes exclusively to stderr (`python -m http.server`, for
instance) therefore shows an empty tail; when that happens and the `.err.log`
is non-empty, one note points at it: `(no stdout output; stderr has output — try -f)`.
A daemon that has never run is a note and exit 0, not a failure.

**`browse`** substitutes `browse_port` for this workspace, checks something
is actually listening on the port (a quick TCP dial — the socket is asked
directly, so a hand-started server counts and a daemon that died during
boot does not), and opens `http://localhost:<port>` with `xdg-open`,
detached. Nothing listening is a refusal that names the `chaban up` to
run and hands over the URL for when the app serves. With no `xdg-open` on
`PATH` it prints the URL and exits 0 — on a remote box, printing *is* the
feature. With one project checked out it needs no argument; with several it
asks you to pick.

Daemons get their own **process group** (that is what makes group stop
possible) but deliberately **not their own session** — no `setsid`. They are
released by the CLI and reparented to init, with stdio already redirected to
their log files.

---

## Sessions

```sh
chaban chat DEMO-1                      # start or resume the agent in the workspace dir
chaban chat DEMO-1 -R                   # ... always start fresh
chaban chat DEMO-1 --model opus         # anything else goes to the agent
chaban launch DEMO-1 "try the tool" demo -- --model opus
chaban try websocket draft -- --model opus
```

(`--model opus` is a Claude Code flag. chaban does not read it.)

**The `agent:` block** in `config.yml` is the session command. chaban knows no
agent's flags; Claude Code's details live in one built-in block (below).

```yaml
agent:
  start: <command for a fresh session>           # required
  resume: <command that continues the last one>  # optional, with can_resume
  can_resume: <exit 0 = resume, exit 1 = start>  # optional, with resume
  instructions_file: AGENTS.md                   # optional, the default
```

`resume` and `can_resume` come as a pair: one without the other is a config
error (exit 4), reported with the other validation errors. Without the pair,
`chat` always runs `start`. `agent:` with nothing under it counts as no block;
`agent: {}` is a block without `start`, an error. The three commands get no
`${…}` substitution: the session environment already exports `WORKSPACE`,
`PORT0`, … as real variables, so write `$WORKSPACE`.

**Start or resume.**

1. `-R`/`--no-resume`, or a block without `resume`: run `start`.
2. Otherwise run `can_resume` in the workspace dir, with the session
   environment, stdin `/dev/null` and stdout discarded. The convention is
   `test`'s and `grep -q`'s: **exit 0** runs `resume`, **exit 1** runs
   `start`.
3. Anything else starts **nothing**: another exit code, death by a signal, a
   failure to spawn, or no exit within **5 seconds**. chaban prints how the
   check failed with the check's own stderr, for example
   `can_resume failed with exit 3: <stderr> (-R starts fresh without the check)`,
   and exits 1. A broken check is reported, never papered over with a fresh
   start; `-R` is the way past it. The check runs in its own process group,
   and the whole group is killed when it ends or times out, so a background
   child it leaves behind cannot stall `chat`.

The chosen command runs as `$SHELL -lc <command>` (fallback `/bin/sh`, as
for every config command) in the workspace dir, with inherited stdio and the
session environment (see *Two tiers*). Its exit code becomes chaban's (see
*Exit codes*).

**Arguments.** Each extra argument is appended to the command line wrapped in
single quotes, with `'` and `\` placed outside the quotes, escaped. sh, bash,
zsh and fish all read that back as exactly one argument, verbatim: spaces,
`$`, `*`, quotes, backslashes and newlines survive. Other shells are not
supported for passthrough (see *Limits*).

**Flags.** chaban's one session flag is `-R`/`--no-resume`, recognized
anywhere **before** the first literal `--`. Everything else is the agent's,
verbatim: flags chaban does not know, and everything after the first `--`
(which is dropped), including a later `--` and strings spelled exactly like
`-R`. For `launch` and `try`, the agent's arguments are only those after
`--`, since their own positionals come before it. The old flags are usage
errors (exit 2) before `--`, each naming what replaced it, so they cannot
reach the agent by accident: `-S` and `--claude-no-skip-permissions`
("skip-permissions is no longer injected; it is whatever your agent:
commands say") and `--claude-no-resume` ("use -R/--no-resume"). After `--`
they are ordinary agent arguments. `chaban claude` is a hidden command that
exits 2 with "`chaban claude` is now `chaban chat`".

The **workspace identifier must come first**: with flag parsing off,
`chaban chat --json DEMO-1` would otherwise resolve a workspace literally
named `--json`, so a leading flag-looking token is a usage error (exit 2)
that says so.

**No `agent:` block: the Claude fallback.** The built-in Claude block is one
embedded YAML text. It is the fallback, the sample chaban prints, and the
commented example in the starter config (a test keeps the stub's copy
identical):

```yaml
agent:
  start: claude
  resume: claude --continue
  can_resume: find ~/.claude/projects/$(pwd | sed 's/[^A-Za-z0-9]/-/g') ~/.claude/projects/$(pwd -P | sed 's/[^A-Za-z0-9]/-/g') -maxdepth 1 -name '*.jsonl' 2>/dev/null | grep -q .
  instructions_file: CLAUDE.md
```

- **`claude` on PATH**: `chat` uses this block and prints to stderr, every
  time, before the agent starts:
  `chaban: no agent: in config.yml, starting claude (found on PATH); add an agent: block to choose`.
  Every time, because chaban keeps no state to show it once.
- **No `claude` on PATH**: chaban prints the block as a sample and exits 4.

The lookup uses chaban's own `PATH` (self-sanitized at startup), for `chat`,
`launch` and `try` alike; a `PATH` set in config `env:` does not count here.
`launch` and `try` decide this **before** they create or check out anything,
so a missing agent leaves no `TRY-<n>` draft or half-made workspace behind. A
*configured* agent whose program is missing is not checked up front (chaban
cannot know which word of a shell string is the program): the shell reports
it and exits 127, which chaban passes on.

The block has no `--dangerously-skip-permissions`: chaban injects no flags.
To skip permissions, copy the block into `config.yml` and add the flag to
`start` and `resume`.

Its `can_resume` line looks for Claude Code's history:
`~/.claude/projects/<encoded dir>/*.jsonl`, where the encoding maps **every
non-alphanumeric character to `-`** (verified against real Claude Code state,
not guessed; non-ASCII paths are unverified). It checks the directory as written (`pwd`) and its
symlink-resolved form (`pwd -P`), so a root reached through a symlink still
finds its history. The pipeline's status is `grep`'s: 0 when a `.jsonl`
exists, 1 when none does, including when neither folder exists. It uses
`find … | grep -q .` rather than a glob because fish prints an error for a
glob that matches nothing. It works under sh, bash, zsh and fish (3.4 or
later, for `$(…)`). Under another shell, write your own.

**`exec`'s project sniff, and the `--` rule.** In
`chaban exec <ws> [project] <cmd…>` the argument right after the workspace
is the **project** if (and only if) it names a configured project; otherwise it
*is* the command. To run a command that happens to be named like a project,
put `--` in that one slot (`chaban exec T-1 -- app`) or give a path
(`./app`). `--` is the sniff suppressor **only** in that position: a later
one belongs to your command and is passed through untouched, so
`chaban exec T-1 app git checkout -- README` still restores a file.

**Titles.** A session names its terminal (OSC escape, when stdout is a
terminal, first 40 characters of the workspace name) and, inside tmux, its
own window (`tmux rename-window -t $TMUX_PANE`, first 20 characters of the
workspace name) — and un-sets that window's `automatic-rename` when the
session ends, so tmux auto-naming resumes exactly where it left off. Only
the session's own window is renamed, never whichever window you happen to
be looking at. Best-effort: no tmux, no `$TMUX_PANE`, no tty, no problem.

**`launch`** composes the daily entry sequence — create-or-reuse, check out,
then hand over the terminal — by calling the same work functions the
individual commands use, so it cannot drift from them. Any phase that fails
stops the sequence, so a session never opens onto a half-built environment.
**Daemons are lazy**: `launch` starts none of them, on either the create or
the reuse path, and reuse does not converge a dead one back to running —
`chaban up <ws> [target…]` is the explicit start, and the skill/session is
expected to call it for whatever it actually needs. Reuse **ignores a supplied
description silently** (the `using existing workspace <name>` line is the
notice), and positional 2 is *always* the description slot on both paths — so
when it happens to name a configured project, a note says what became of it,
because you almost certainly meant `launch <id> <desc> <project…>`. On the
**create** path, `launch` does not print `new`'s `hint: chaban cd <id>` —
the terminal is about to become the session's, so a `cd` hint would read as a
stale to-do — and instead prints `tip: in another terminal: chaban cd <id>
— work alongside this session`. The reuse path prints neither.

### The instructions file

`new` (and so `launch` and `try`) and `adopt` write one file at the
workspace root, once: a pointer to `WORKSPACE.md` and the seeded `## Status`
frame. Its name is `instructions_file`: the configured one, `CLAUDE.md`
with no `agent:` block (the built-in block's), or `AGENTS.md` when a block
leaves it out or empty. It must be a plain file name: one containing `/` or
`\`, or one that is `.` or `..`, is a config error (exit 4). Resolving the name needs no agent on
`PATH`: with no block, `new` and `adopt` write `CLAUDE.md` whether or not
`claude` is installed.

The file is created only when absent and then never touched again, so the
agent's notes survive every regeneration. Everything that names it uses the
configured name:

- `status` reads its `## Status` section and labels it
  `status (from <file>):`;
- `WORKSPACE.md` says "keep durable notes in `<file>` instead";
- `chaban which --instructions-file` prints its absolute path (whether or
  not it exists), and `which --json` carries the same path as
  `instructions_file`;
- the SessionStart hook asks `which --instructions-file` which file to check
  for a `## Status` section.

A workspace keeps the file it was born with. Change `instructions_file`
later and existing workspaces still hold the old file: `status` reads only
the configured name, and when that file is missing it says
`no <file> in this workspace` rather than showing nothing.

---

## Workspace hygiene

Four commands, and one rule that runs through all of them: **the tool never
deletes a directory it did not create.**

- **`adopt [dir]`** gives an existing tree an allocation — an index, values, a
  `.env` per project, `WORKSPACE.md`, the instructions file if absent — and records it as
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
| Merged | every checked-out project's **actual** branch (read from the worktree, whatever it is named) is fully merged into its base | any unanswerable merge question — unreadable branch, missing base, moved repo, detached HEAD — reads as *not merged* |
| Clean | no checked-out worktree is dirty (modified or untracked) | dirty, and **loudly**: `skipped <name> (uncommitted changes)`. `destroy` keeps the power to discard uncommitted work; a batch sweep does not. |

The base for the merge check is the project's `base_branch`, or — when that is
empty — the **source repo's own HEAD branch at gc time**, mirroring what
`checkout` branched from. (The branch is compared refs/heads-qualified while the
base is taken unqualified: a documented asymmetry.) The clean check is the same
predicate `ls -g` renders — which is why an untracked `.env` in a repo that does
not gitignore it makes `gc -d` collect nothing, tool-wide. One definition of
dirty, and the fix lives in the repo.

**A live agent session does not protect a workspace.** The daemon gate reads
pid files, and a session (or a plain shell) sitting in the directory writes
none — so a merged, clean, daemonless workspace can be collected out from under
one. A session pid marker is future work; until then, `gc -d` is a deliberate
act, and an unmerged or dirty workspace is safe by the gates above.

**`doctor`** reports and never fixes (`gc` and `down` fix). It prints the config
verdict, the configured agent (`agent: <start>`, when there is a block), the
configured projects and value blocks, then: with no `agent:` block, a note
that `chat` falls back to `claude` (and a finding when `claude` is not on
`PATH` either, since `chat` could start nothing), stale allocations
(dir gone), allocations **outside the root** — informational for an adopted
workspace, a finding for a tool-created one, which is a corrupt or hand-edited
registry and something `destroy` will refuse — configured projects whose `repo`
does not exist, unresolvable `browse_port` templates, unregistered dirs in the
root, daemons without descriptions (a note — sessions decide what to start by
them), and per-workspace daemon health from the pids directory (live counts,
stale records, keys the config cannot name). It ends with
`doctor: N finding(s)` or `doctor: no findings` and **exits 0 regardless**:
findings are observations, not failures. Only an invalid config (exit 4) and an
unreadable registry (exit 1) are errors. `--json` gives `findings` and
`informational` arrays with a stable `kind` per entry.

---

## Odd corners

- **Symlinks.** A workspace directory reached through a symlink reads as "not
  checked out", and `which` compares paths **as written** (no `EvalSymlinks`) —
  the registry records a spelling, and the shell wrapper `cd`s to the spelling
  the tool printed, so the two agree by construction. Reaching the same
  directory by another route is honestly reported as "not inside a workspace".
  (The built-in Claude `can_resume` line is the one place that tries both
  spellings, because Claude Code records whichever it ran in.)
- **State.** One registry, `<root>/.allocations.json` (JSON, per-root, flock'd,
  atomically written), and nothing else. Everything else is derived on demand:
  no status machine, no "broken workspace", no recorded project list.
  `WORKSPACE.md` — the task, the allocated values, each project's dir/branch,
  the per-project `instructions`, and each project's daemons with their
  descriptions — is regenerated wholesale; the instructions file is written
  once and then never touched, so notes accumulated there survive every regeneration.
- **Boundaries.** No Docker runtime (a container-based flavor is on the
  roadmap), no Windows (POSIX: flock, process groups, `$SHELL -lc`), no
  profiles (named `start:` subsets) yet.

---

## A root other than `~/workspaces`

`CHABAN_ROOT_DIR` points chaban at another root. Each root is independent:
its own `config.yml`, registry and workspaces. Coming from claude-workspaces,
whose root was `~/claude-workspaces`, read
[Upgrading from claude-workspaces](upgrading-from-claude-workspaces.md)
first: it covers the switch and the two guards that catch an old setup.

### The variable must reach every process

If you set `CHABAN_ROOT_DIR`, every process that runs `chaban` needs it.

- User commands (`setup`, `start`, `stop`, `teardown`, `exec`) get the
  curated env, which does not carry it. If those commands call `chaban`, add
  `CHABAN_ROOT_DIR` to `env_allow` in `config.yml` (a list of names, global or
  per project).
- An agent launched outside your shell (an IDE, a desktop app) does not
  see a shell-only variable. Set it session-wide instead, for example in
  `~/.config/environment.d/`.

### Moving a root

Don't. Use `CHABAN_ROOT_DIR`. A root with live worktrees breaks when moved:
each source repo keeps stale worktree records, `destroy` fails, and
`git worktree prune` from `destroy --force` orphans the other moved
worktrees. Claude session history is keyed by absolute path and stays with
the old path.

If you must: `chaban down` every workspace, move the directory, then in each
source repo run `git -C <repo> worktree repair <new worktree paths...>`.
Accept that the session history stays behind.
