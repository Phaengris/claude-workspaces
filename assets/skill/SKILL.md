---
name: claude-workspaces
description: >-
  Create and drive isolated dev workspaces with the `workspace` CLI — a git
  worktree per project, its own port block, its own env and its own daemons.
  Use when the user says "create a workspace", "work on TASK-123", "spin up a
  workspace for this ticket", "start/stop the services", "tail the rails log",
  "what's running", "destroy the workspace", or when the session is already
  inside a workspace directory and needs its identity, ports or processes.
---

# claude-workspaces

`workspace` gives one task its own runnable copy of the stack. A workspace is
a directory under the root (`~/claude-workspaces` unless
`CLAUDE_WORKSPACES_ROOT_DIR` says otherwise) holding:

- **a git worktree per checked-out project** — its own branch, named after the
  task id, so parallel tasks never share a checkout;
- **an allocation index**, which derives the workspace's numbered values
  (`PORT0`, `PORT1`, … from `values:` in config) — no two live workspaces get
  the same ports;
- **an env** (global `env` + per-project `env`, values substituted), seeded
  into each worktree's `.env`;
- **daemons** — the project's `start:` entries, with pid files and logs under
  `.workspace/`.

Nothing is recorded that can be derived: whether a project is checked out is
what git says, whether a daemon runs is whether its pid is alive. Every
command is an idempotent ensure — re-running is always safe.

`workspace --help` and `workspace <cmd> --help` are the reference for flags
and arguments. This file is the map.

## Lifecycle

**The one-shot.** For "work on TASK-123", prefer:

```
workspace launch TASK-123 "short description" project-a project-b
```

`launch` = create-or-reuse the workspace → check the listed projects out →
open a Claude Code session in the workspace dir. **Daemons are not
started.** An existing workspace is reused as-is (the description is
ignored; listed projects are checked out). Pass session flags after `--`.

**The steps, when you need them individually:**

| Step | Command |
|---|---|
| create a workspace | `workspace new <task_id> <description> [project…]` |
| add a project to it | `workspace checkout <ws> <project…>` |
| run setup + start daemons | `workspace up <ws> [target…]` |
| start one service | `workspace up <ws> <daemon>` |
| stop daemons | `workspace down <ws> [target…]` |
| stop then start again | `workspace restart <ws> [target…]` |
| see what is true right now | `workspace status [<ws>]`, `workspace ls`, `workspace ports` |
| read a daemon's log | `workspace logs <ws> <daemon> [-f] [-n N]` |
| run a command inside it | `workspace exec <ws> [project] <cmd…>` |
| open the app | `workspace browse <ws> [project]` |
| resolve a directory | `workspace cd <ws> [project]` (prints the path; the installed shell wrapper does the chdir) |
| where am I? | `workspace which` |
| tear it all down | `workspace destroy <ws>` |

A workspace is addressed by its full directory name or by its task id.
`up`/`down`/`restart`/`logs` targets are project names, daemon names, or
`project:daemon` when a daemon name is ambiguous; no target means the whole
workspace.

Exit codes are meaningful: 0 fine, 1 the operation failed, 2 usage, 3 no such
workspace/project, 4 the config is broken. `workspace doctor` explains a 4 and
reports registry/allocation/daemon health.

## Working inside a workspace

`WORKSPACE.md` in the workspace dir is the identity file: task id,
description, the allocated values (`PORT0=…`), each checked-out project's
directory and branch, and the per-project `instructions:` from config. Read it
first — the ports and the project layout are there, and its instructions are
the project owner's words about that project. It is regenerated on every
`new`/`checkout`, so never edit it; durable notes belong in `CLAUDE.md` next
to it, which the tool writes once and then never touches.

`workspace status <ws>` is the live view: which projects are checked out, on
what branch, whether setup is current, which daemons are running.

## Services are lazy

Nothing starts a daemon until you ask. The session-start status block (and
`workspace status <ws>`) lists every configured daemon, whether it runs,
and — when the config describes it — what it is for:

    rails: stopped — app server — UI at http://localhost:20800

Start exactly what the task needs (`workspace up <ws> rails`), check it
with `workspace logs <ws> rails`, and `workspace down <ws>` when done.
WORKSPACE.md carries the same service list per project.

## The environment contract

Commands the tool spawns (`setup`, `start`, `stop`, `teardown`, `exec`, the
Claude session) do **not** inherit the ambient shell environment. They get a
curated one: a fixed allowlist of safe parent vars, a PATH with per-version
install directories stripped so version-manager shims resolve each worktree's
own `.ruby-version`/`.tool-versions`, plus the workspace and project `env` on
top. Consequences worth remembering:

- a variable that is not on the allowlist silently does not arrive — the fix
  is `env_allow:` in config (global or per project), or an `env:` entry;
- so run things through the tool: `workspace exec <ws> <project> <cmd…>`
  reproduces exactly what daemons see. A bare shell command in the worktree
  does not.
- `workspace env <ws> [project]` prints the resolved environment when
  something looks wrong.

## Hygiene

- `workspace down <ws>` when you are done working but keeping the workspace.
- `workspace destroy <ws>` runs `teardown:`, removes the worktrees, the
  directory and the allocation. It only removes directories the tool created.
- `workspace adopt [dir]` / `workspace release [dir]` provision or free an
  allocation for a checkout the tool did not create; `release` never deletes
  files.
- `workspace gc` releases allocations whose directories are gone and clears
  stale pid files. `workspace gc --destroy-dirs` additionally destroys
  tool-created workspaces that are fully merged, clean and idle — the routine
  way to reclaim finished tasks.

Never hand-edit the registry (`<root>/.allocations.json`) or pid files; use
the commands, and `workspace doctor` when the state looks impossible.
