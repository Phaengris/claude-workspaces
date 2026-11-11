# Agent-neutral sessions: `chaban chat` and the `agent:` block

Status: approved design, 2026-11-09. Ships in 2.0.0 together with the rename
to chaban (2.0.0 is not tagged yet). Supersedes spec §8's injection matrix
(`docs/superpowers/specs/2026-07-30-claude-workspaces-go-design.md`).

## Why

chaban starts the AI session itself (`claude`, `launch`, `try`), and that
code is written for Claude Code only: it runs the `claude` binary, injects
`--dangerously-skip-permissions` by default, decides `--continue` by reading
Claude's history folder, and parses Claude's own flags (`--permission-mode`,
`-p`, `--resume`, …) so its injections don't fight the user's.

Two problems with that:

- Other people use other agents. The tool should run whatever agent the
  user configures.
- Skipping permissions is a decision the user should make visibly, in their
  config, not a hidden default.

## Goals

- The session command comes from config. chaban knows nothing about any
  agent's flags.
- Claude Code keeps working with no config, through a visible fallback.
- `--dangerously-skip-permissions` appears only where a user wrote it.

## Non-goals

- The other Claude integrations stay Claude-only: the SessionStart hook, the
  statusline snippet, the skill in `~/.claude/skills`. Other agents get a
  working launcher and the instructions file (below), without the hook's
  per-session context.
- No per-project agent. One session spans every project in a workspace, so
  the block is global.
- No migration code. The upgrade notes tell users what to add.

## Anti-goals

- **Hidden behavior.** chaban never starts an agent the user didn't choose
  without saying so. (The user's words: "unexpected behavior/hidden reasons
  is bad".)
- **Agent knowledge in Go.** No Claude flag parsing, no Claude history probe
  in chaban's code after this change. Claude-specific knowledge lives in one
  embedded YAML text (below).

## The `agent:` block

Top level of `config.yml`, decoded strictly like everything else:

```yaml
agent:
  start: claude --dangerously-skip-permissions
  resume: claude --dangerously-skip-permissions --continue
  can_resume: <shell command: exit 0 = resume, exit 1 = start fresh>
  instructions_file: CLAUDE.md
```

- `start` is required when the block is present.
- `instructions_file` is optional; default `AGENTS.md` (the file most agents
  read: Codex, Cursor, Copilot, and Claude Code ≥ 2.1.277 as a fallback when
  no CLAUDE.md exists). A bare file name, no `/`; validated (exit 4). See
  "The instructions file".
- `resume` and `can_resume` come as a pair: both or neither. One without the
  other is a config validation error (exit 4), reported with the other
  validation errors. Without the pair, chat always runs `start`.
- All three are shell command strings, run the way every other config
  command runs: `$SHELL -lc <command>` (fallback `/bin/sh`, as `proc.Run`).
  Users write them in their own shell's syntax, as they do for
  `setup:`/`start:`, so `$WORKSPACE`, `~` and `&&` work here too.
  - Decided with the user after weighing plain (no-shell) command lines:
    consistency with every other config command won. The login shell's
    startup runs before the agent, the same documented limit as for project
    commands (reference, "Limits").
- No `${…}` runtime-token substitution in these fields. The session env
  already exports `WORKSPACE`, `PORT0`, … as real variables, so plain shell
  expansion (`$WORKSPACE`) covers it. (My call: fewer moving parts, and
  `${PROJECT}` would have no meaning here.)

## `chaban chat <workspace> [-R] [args…]`

Replaces `chaban claude`.

1. Resolve the agent block: from config, or the fallback (next section).
2. If `-R` was given, or the block has no `resume`, run `start`.
3. Otherwise run `can_resume` in the workspace dir, the convention of `test`
   and `grep`:
   - **exit 0** → run `resume`;
   - **exit 1** → run `start`;
   - **anything else**: another exit code, failed to spawn, or no answer
     within **5 seconds** → chaban starts **nothing**. It prints that
     `can_resume` failed, how (`exit 127`, `timed out after 5s`, the spawn
     error), and the check's own stderr, then exits 1. `-R` is the way past
     it, since it skips the check. (Decided with the user: a broken check is
     reported, never papered over with a fresh start.)
   - Its stdin is `/dev/null`; stdout is discarded; stderr is captured for
     the failure message.
4. Extra arguments are appended to the chosen command string, each quoted so
   it reaches the agent as exactly one argument, verbatim, under sh, bash, zsh
   and fish. One scheme covers all four: wrap each argument in single quotes,
   and emit `'` and `\` outside the quotes, escaped (`\'`, `\\`). Pin it with
   tests per shell present on the machine; arguments containing quotes,
   backslashes, spaces, `$`, `*` and newlines must all survive. Other shells
   (tcsh, nushell, …) are not supported for argument passthrough; the
   reference says so in one line.
5. The command runs in the workspace dir, with inherited stdio, under the
   **session environment** (today's `sessionEnv`: sanitized inherited env +
   workspace overlay), not the curated one. `can_resume` gets the same env.
6. Unchanged from today: signal absorption while the agent runs, session
   titles (tmux window / OSC), the child's exit code becomes chaban's
   (`xerr.Exit`).
   - Changed by the shell in between: when the agent is killed by a signal,
     what chaban sees depends on the shell. bash replaces itself with a
     simple command, so chaban still sees a signal death (plain exit-1
     error). fish, and bash running a compound command, stay as the parent
     and exit 128+n, which chaban passes on. Accepted; the reference
     documents it. Tests pin exit codes only for normal exits.

Flags, scanned only before a literal `--` as today:

- `-R` / `--no-resume`: always `start`. (My call: the old long form
  `--claude-no-resume` names Claude.)
- `-S`, `--claude-no-skip-permissions`, `--claude-no-resume`: removed. Each
  is a usage error (exit 2) whose message says what replaced it: for `-S`,
  "skip-permissions is no longer injected; it is whatever your `agent:`
  commands say"; for `--claude-no-resume`, "use `-R`/`--no-resume`".
  Without this they would silently reach the agent as arguments.
- After `--`, everything goes to the agent verbatim (unchanged).
- The first positional must be the workspace (`requireIdentFirst`,
  unchanged).

`chaban claude` itself: a hidden command that fails with a usage error
(exit 2): "`chaban claude` is now `chaban chat`". (My call: a plain "unknown
command" would leave the user guessing.)

`launch` and `try` use the same session path, same flags; their usage text
says `-- agent args…` instead of `-- claude args…`.

**Check before creating.** `launch` and `try` resolve the agent block
(including the fallback decision and the "no `claude` on PATH → exit 4"
case) **before** they create or check out anything, so a missing agent never
leaves a stray `TRY-<n>` draft or a half-made workspace behind. The PATH
looked up is chaban's own (already self-sanitized at startup), since the
session env needs a workspace to exist; a PATH set in config `env:` is
ignored for this one check. (My call.) A *configured* agent whose program is
missing is not checked up front (chaban can't know which word of a shell
string is the program); the shell reports it and exits 127, which chaban
passes on.

## No `agent:` block: the Claude fallback

The built-in Claude block lives in **one embedded YAML text** (an asset
file). The same text is:

- the fallback chat uses when config has no `agent:` block,
- the sample printed when there is no `claude` on PATH,
- the commented-out example in `assets/config_stub.yml`.

A test pins that the stub's commented block and the asset are the same text,
so they can't drift.

The fallback block (no skip-permissions; that's the point):

```yaml
agent:
  start: claude
  resume: claude --continue
  can_resume: find ~/.claude/projects/$(pwd | sed 's/[^A-Za-z0-9]/-/g') ~/.claude/projects/$(pwd -P | sed 's/[^A-Za-z0-9]/-/g') -maxdepth 1 -name '*.jsonl' 2>/dev/null | grep -q .
  instructions_file: CLAUDE.md
```

`instructions_file: CLAUDE.md` is in the built-in block so that anyone who
copies the sample to add their own flags keeps their existing workspaces'
file name; the upgrade note says to start from the sample.

The pipeline's status is `grep`'s: 0 when a `.jsonl` exists, 1 when none
does (including when neither folder exists), so it fits the exit-code
convention above.

The `can_resume` line is the shell version of today's Go probe
(`hasConversation`): Claude names a project's history folder after the
directory with every non-alphanumeric byte turned into `-`, and the line
checks both the path as written (`pwd`) and the resolved path (`pwd -P`),
as the Go code does today. It runs under the user's `$SHELL`, so it must
work unchanged under sh, bash, zsh and fish (fish ≥ 3.4 for `$(…)`); users
of other shells write their own, as they do for `setup:`. That's why it uses `find … | grep -q .` rather than
a glob: fish prints an error for a glob that matches nothing. The builder
verifies the line under each shell present on the machine, against a fake
`~/.claude/projects` tree, including a workspace path reached through a
symlink and a path with `_` in it.

Behavior with no `agent:` block:

- **`claude` is on PATH** (looked up in the session env's PATH): use the
  built-in block and print to stderr, every time, before the agent starts:

  ```
  chaban: no agent: in config.yml, starting claude (found on PATH); add an agent: block to choose
  ```

  Every time, because chaban keeps no state to show it once, and the user
  asked that the fallback never be silent.
- **No `claude` on PATH:** print the built-in block as a sample with one line
  of explanation, exit 4 (config error).

## The instructions file

Today chaban writes `CLAUDE.md` once at the workspace root (`new`, `adopt`;
`wsp.EnsureClaudeMD`), seeded with the pointer to WORKSPACE.md and the
`## Status` frame, and never touches it again. That file name becomes the
resolved `instructions_file` (configured, or the fallback block's
`CLAUDE.md`, or the default `AGENTS.md`). Everything that names the file
uses the resolved name:

- the generated file itself (`EnsureClaudeMD` → takes the name; write-once
  and O_EXCL semantics unchanged);
- `chaban status`, which reads the `## Status` section from it, and its
  "status (from <file>)" label;
- WORKSPACE.md's hint "keep durable notes in <file> instead";
- the SessionStart hook's "no `## Status` section" nag. The hook is a shell
  script and can't read config, so it gets the name from chaban (the builder
  picks how: e.g. a field in `status --json`, or `chaban which` growing an
  option). The hook still exits 0 on every path.
- the skill: it refers to "the workspace's instructions file (WORKSPACE.md
  names it)" instead of `CLAUDE.md`;
- README and reference wording.

Resolving the name needs the agent block but not the Claude fallback's PATH
check: a workspace created with no `agent:` block gets `CLAUDE.md` whether
or not `claude` is installed.

A workspace keeps the file it was born with. If the user later changes
`instructions_file`, existing workspaces still hold the old file;
`status` reads only the configured name and, when that file is missing,
says so in one line ("no <file> in this workspace") rather than showing
nothing. (My call: no guessing among known names in Go, and no silent gap.)

## Other surfaces

- `chaban doctor`: report "no `agent:` block: chat falls back to claude" as
  an informational line, and a missing `claude` in that case as a finding.
  (My call; small, and it's where a user looks when chat misbehaves.)
- Completions: `chat` completes workspace names exactly as `claude` did.
- Docs, in the same change: README command table and examples, reference
  (the session section, exit codes, the "everything after `--`" rules), the
  skill (`assets/skill/SKILL.md`), the config stub, CHANGELOG under
  Unreleased with an upgrade note: "copy the Claude sample into an `agent:`
  block (it keeps `instructions_file: CLAUDE.md`); put
  `--dangerously-skip-permissions` there if you want it back."
- The statusline is untouched. The hook changes only as described in "The
  instructions file".

## Removed code

`buildClaudeArgv`, `hasConversation`, `conversationRecorded`,
`encodeClaudeProjectDir` and their tests go away; their knowledge moves into
the embedded YAML text. `extractSessionFlags` shrinks to `-R` plus the
removed-flag errors. The claude.txtar encoding cross-check becomes a test of
the `can_resume` line.

## Testing

- No real agent ever runs in tests (existing rule: PATH shims for `claude`).
- testscript: chat with a configured fake agent (start vs resume chosen by
  `can_resume`'s exit code; `-R`; args with hostile characters arriving
  verbatim, under each available shell; exit code propagated);
  launch/try with no `claude` and no `agent:` create nothing (registry and
  root unchanged, exit 4); the fallback with a `claude` shim (stderr
  notice present); no `claude` on PATH (sample printed, exit 4 pinned in Go);
  removed flags and `chaban claude` (exit 2 pinned); the pair validation
  (exit 4); `can_resume` exit 0 / 1 / other / timeout (other and timeout:
  nothing started, exit 1, message carries the check's stderr);
  `instructions_file` default, configured and fallback names reaching the
  generated file, `status`, WORKSPACE.md and the hook; `status` on a
  workspace whose file is missing.
- The fallback `can_resume` line under each available shell, as above.
- The stub/asset equality test.

## Left to the builder

- Package placement (likely the session code moves from `claude.go` to
  `chat.go`; config gets an `Agent` struct).
- Exact wording of messages other than the fallback notice.
