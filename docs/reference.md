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
matching case-insensitively. A completer never reports a failure: a broken
config or registry collapses to "no suggestions" rather than printing an error
at your prompt or falling back to file names in a workspace slot. The two
session commands (`claude`, `launch`) disable flag parsing so everything can
reach Claude, which also means the shell can only be helped with their first
argument. (`completion powershell` comes free from the CLI framework and is
untested here.) `ls -g`'s git stats run concurrently, bounded.

**Exit codes.** A `claude`/`launch` session propagates **Claude's own exit
code verbatim**, so a 3 from there is Claude's 3, not "not found".

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
detached. Nothing listening is a refusal that names the `workspace up` to
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
  state, not guessed). Both the directory as recorded and its symlink-resolved
  form are probed, so a workspaces root reached through a symlink still finds
  its history. Every failure to look is "no history", and the safe failure
  direction is a **fresh session instead of `--continue`** — never the reverse.
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

**Titles.** A session names its terminal (OSC escape, when stdout is a
terminal, first 40 characters of the workspace name) and, inside tmux, the
current window (`tmux rename-window`, first 20 characters of the workspace
name) — and un-sets the window's `automatic-rename` when the session ends, so
tmux auto-naming resumes exactly where it left off. Best-effort: no tmux, no
tty, no problem.

**`launch`** composes the daily entry sequence — create-or-reuse, check out,
then hand over the terminal — by calling the same work functions the
individual commands use, so it cannot drift from them. Any phase that fails
stops the sequence, so a session never opens onto a half-built environment.
**Daemons are lazy**: `launch` starts none of them, on either the create or
the reuse path, and reuse does not converge a dead one back to running —
`workspace up <ws> [target…]` is the explicit start, and the skill/session is
expected to call it for whatever it actually needs. Reuse **ignores a supplied
description silently** (the `using existing workspace <name>` line is the
notice), and positional 2 is *always* the description slot on both paths — so
when it happens to name a configured project, a note says what became of it,
because you almost certainly meant `launch <id> <desc> <project…>`. On the
**create** path, `launch` does not print `new`'s `hint: workspace cd <id>` —
the terminal is about to become the session's, so a `cd` hint would read as a
stale to-do — and instead prints `tip: in another terminal: workspace cd <id>
— work alongside this session`. The reuse path prints neither.

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
| Merged | every checked-out project's **actual** branch (read from the worktree, whatever it is named) is fully merged into its base | any unanswerable merge question — unreadable branch, missing base, moved repo, detached HEAD — reads as *not merged* |
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
one. A session pid marker is future work; until then, `gc -d` is a deliberate
act, and an unmerged or dirty workspace is safe by the gates above.

**`doctor`** reports and never fixes (`gc` and `down` fix). It prints the config
verdict, the configured projects and value blocks, then: stale allocations
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
  (The Claude history probe is the one place that tries both spellings, because
  Claude Code records whichever it ran in.)
- **State.** One registry, `<root>/.allocations.json` (JSON, per-root, flock'd,
  atomically written), and nothing else. Everything else is derived on demand:
  no status machine, no "broken workspace", no recorded project list.
  `WORKSPACE.md` — the task, the allocated values, each project's dir/branch,
  the per-project `instructions`, and each project's daemons with their
  descriptions — is regenerated wholesale; `CLAUDE.md` is written once and
  then never touched, so notes accumulated there survive every regeneration.
- **Boundaries.** No Docker runtime (a container-based flavor is on the
  roadmap), no Windows (POSIX: flock, process groups, `$SHELL -lc`), no
  profiles (named `start:` subsets) yet.
