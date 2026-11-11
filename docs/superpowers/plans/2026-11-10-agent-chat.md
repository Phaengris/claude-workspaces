# Agent-neutral sessions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `chaban claude` with `chaban chat`, driven by a config `agent:` block (`start`, `resume`, `can_resume`, `instructions_file`), with a visible Claude fallback and no Claude knowledge left in Go beyond one embedded YAML text.

**Architecture:** `internal/config` gains the `Agent` type, its validation, and the embedded Claude block (the single home of Claude-specific knowledge). `internal/cli/session.go` holds the agent-neutral session machinery (flag parsing, argument quoting, the `can_resume` probe, agent resolution, the spawn); `chat`, `launch` and `try` call it. The generated instructions file, `status`, `which`, WORKSPACE.md and the SessionStart hook take the resolved file name from config.

**Tech Stack:** Go 1.26, cobra, goccy/go-yaml (strict decode), testscript txtar.

**Spec:** `docs/superpowers/specs/2026-11-09-agent-chat-design.md` (approved 2026-11-10). Read it in full before your task; it is the authority where this plan is silent.

## Global Constraints

- Branch `agent-chat`. Conventional commits; breaking changes marked `!`. Every commit ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Before every commit: `gofmt -l .` prints nothing, `go vet ./...` clean, `go test ./...` green.
- A real `claude` must never be invocable from tests: PATH shims only, prepended first; HOME redirected for anything that reads `~/.claude`.
- Exact exit codes are pinned in Go tests (txtar `! exec` is only "non-zero"): 0 ok, 1 failure, 2 usage, 3 not found, 4 config; a child's code passes through verbatim.
- Mutation-check load-bearing pins and say so in the commit message.
- Agent commands (`start`, `resume`, `can_resume`) run as `$SHELL -lc <command>`, `$SHELL` read from chaban's own env, fallback `/bin/sh`: the same rule as `proc.Run`.
- The session env is `sessionEnv(cfg, ws)` (inherited + overlay), never the curated env, for both the agent and `can_resume`.
- `can_resume` exit codes: 0 → `resume`, 1 → `start`, anything else / spawn failure / 5 s timeout → nothing starts, exit 1.
- Fallback notice, exact text (stderr, one line, every time):
  `chaban: no agent: in config.yml, starting claude (found on PATH); add an agent: block to choose`
- Default `instructions_file` is `AGENTS.md`; the built-in Claude block sets `CLAUDE.md`.
- No `${…}` runtime-token substitution in agent fields.
- Docs that describe behavior change in the same commit as the behavior where practical; Task 6 sweeps the rest.

## Rulings (made while planning; the spec is silent or loose here)

- **One PATH lookup for the fallback.** The "is `claude` on PATH" check uses chaban's own PATH (`exec.LookPath`) for `chat`, `launch` and `try` alike. The spec named the session env's PATH for `chat`; that differs only when config `env:` overrides `PATH`, and one rule beats two. Cost if wrong: a user who sets `PATH` in config `env:` sees the fallback decided on the outer PATH.
- **The fallback's program name is derived, not hard-coded:** the first word of the built-in block's `start`. Keeps Claude knowledge in the YAML text.
- **`agent:` with a null value** (`agent:` and nothing under it) decodes to "no block" and takes the fallback. `agent: {}` is a block without `start` and fails validation.
- **`instructions_file: ""`** means the default (`AGENTS.md`), same as absent.
- **The hook learns the file name from `chaban which --instructions-file`** (new flag; prints the absolute path of the workspace's instructions file, whether or not it exists). `which --json` gains the same value as `instructions_file`.
- **The probe runs in its own process group** (`Setpgid`) and a timeout kills the whole group, with `cmd.WaitDelay` as a backstop, so a grandchild holding the stderr pipe can't hang `chat`.

## Review Focus

1. A `can_resume` command that leaves a background grandchild (`sleep 30 & false`) must not make `chat` wait past the timeout: the group is killed and `chat` returns promptly. Test in Task 2.
2. A workspace root whose path contains a space, or is reached through a symlink, must still resume under the shipped Claude `can_resume` line. Test in Task 1.
3. `-S`, `--claude-no-skip-permissions`, `--claude-no-resume` typed **after** `--` belong to the agent and must pass through, not error. Test in Task 2.
4. `agent:` present but null, `agent: {}`, `instructions_file: ""`, `instructions_file: sub/AGENTS.md`, `..`: each has one defined outcome (rulings above; validation errors are exit 4). Test in Task 1.
5. `launch`/`try` with no `agent:` block and no `claude` on PATH must leave the registry and the root exactly as they were. Test in Task 3.

---

### Task 1: The `agent:` config block and the built-in Claude block

**Files:**
- Modify: `internal/config/types.go` (add `Agent` field + type)
- Modify: `internal/config/validate.go` (agent rules, inside the existing `errors.Join` collection)
- Create: `internal/config/agent.go`, `internal/config/claude_agent.yml`
- Test: `internal/config/agent_test.go`, `internal/config/validate_test.go`
- Modify: `assets/config_stub.yml` (commented agent section)
- Test: `internal/assets/assets_test.go` (stub carries the block)

**Interfaces:**
- Produces:
  - `type Agent struct { Start string \`yaml:"start"\`; Resume string \`yaml:"resume"\`; CanResume string \`yaml:"can_resume"\`; InstructionsFile string \`yaml:"instructions_file"\` }`
  - `Config.Agent *Agent \`yaml:"agent"\``
  - `const DefaultInstructionsFile = "AGENTS.md"`
  - `func (a *Agent) InstructionsFileName() string`: the field, or `DefaultInstructionsFile` when empty.
  - `func ClaudeAgentYAML() []byte`: the embedded text, a copy.
  - `func ClaudeAgent() *Agent`: the parsed built-in block, a fresh copy per call.
  - `func (c *Config) InstructionsFile() string`: `c.Agent.InstructionsFileName()` when a block is configured, else `ClaudeAgent().InstructionsFileName()` (`CLAUDE.md`).

- [ ] **Step 1: Write the failing tests**

`internal/config/agent_test.go`:
- `TestClaudeAgentBlock`: `ClaudeAgent()` has `Start == "claude"`, `Resume == "claude --continue"`, `InstructionsFile == "CLAUDE.md"`, `CanResume` equal to the spec's line verbatim. Mutating the returned struct does not change the next call's result.
- `TestInstructionsFile`: table over configs: no block → `"CLAUDE.md"`; block without the field → `"AGENTS.md"`; `instructions_file: ""` → `"AGENTS.md"`; `instructions_file: GEMINI.md` → `"GEMINI.md"`.
- `TestClaudeCanResumeLine`: for each of `sh`, `bash`, `zsh`, `fish` found by `exec.LookPath` (skip the missing ones with `t.Skip` per subtest): fake HOME with `.claude/projects/<encoded>/x.jsonl` for a workspace dir named `T-1_a_b` under a root whose path contains a space; run `<shell> -lc <CanResume>` with `Dir` = the workspace and env `HOME`, `PWD` set. Expect exit 0 via the real path, exit 0 via a symlinked route to the same dir (PWD = the symlink path), exit 1 for a sibling `T-2_none` without history. Encode in the test with an independent Go function (every byte outside `[A-Za-z0-9]` → `-`).

`internal/config/load_test.go` (or `validate_test.go`, following the file's pattern), one case each, asserting `errors.Is(err, xerr.ErrConfig)` where the load path wraps it:
- `agent:` null → loads, `cfg.Agent == nil`.
- `agent: {}` → error containing `agent: start is required`.
- `resume` without `can_resume`, and the reverse → error containing `agent: resume and can_resume go together`.
- `instructions_file: sub/AGENTS.md`, `.`, `..` → error containing `agent: instructions_file must be a plain file name`.
- A full valid block → loads, fields as written.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/config/`
Expected: compile errors (`Agent`, `ClaudeAgent` undefined).

- [ ] **Step 3: Implement**

- `claude_agent.yml` holds exactly this text (the spec's block, nothing else):
  ```yaml
  agent:
    start: claude
    resume: claude --continue
    can_resume: find ~/.claude/projects/$(pwd | sed 's/[^A-Za-z0-9]/-/g') ~/.claude/projects/$(pwd -P | sed 's/[^A-Za-z0-9]/-/g') -maxdepth 1 -name '*.jsonl' 2>/dev/null | grep -q .
    instructions_file: CLAUDE.md
  ```
- `agent.go` embeds it with `//go:embed`, decodes it once with the same strict decoder `Load` uses into `struct{ Agent *Agent \`yaml:"agent"\` }`, runs the agent validation on it, and panics at init on any error (the test above keeps that from ever firing in a release).
- Validation messages are the exact substrings the tests assert. Check whether template expansion (`expand.go`) touches top-level keys other than `projects`; it must pass `agent:` through untouched.

- [ ] **Step 4: The config stub**

Add an `agent` section to `assets/config_stub.yml`, in the stub's existing style: a short header explaining the block (no block → chat starts `claude` with a notice; `--dangerously-skip-permissions` goes in `start`/`resume` if wanted; `can_resume` exit 0 resume / 1 start; `instructions_file` default `AGENTS.md`), then the built-in block with every line prefixed `# `.

Test `TestConfigStubCarriesClaudeAgent` in `internal/assets/assets_test.go`: every line of `config.ClaudeAgentYAML()`, prefixed `# `, appears in the stub as consecutive lines in order. The existing `TestConfigStubExamplesAreValidWhenUncommented` must still pass with the block uncommented (it now also validates the block). Mutation-check: change one character in the stub's copy, see the new test fail.

- [ ] **Step 5: Run and commit**

Run: `gofmt -l . && go vet ./... && go test ./...`
Expected: all green.

```bash
git add internal/config assets/config_stub.yml internal/assets/assets_test.go
git commit -m "feat(config): agent: block and the built-in Claude block"
```

---

### Task 2: Session building blocks

New file `internal/cli/session.go`, alongside the old code (nothing is wired or deleted yet; Task 3 does that). Keeps the tree green between tasks.

**Files:**
- Create: `internal/cli/session.go`, `internal/cli/session_test.go`
- Modify: `internal/proc/proc.go`, `internal/proc/daemon.go` (extract `Shell()`)

**Interfaces:**
- Consumes: `config.Agent`, `config.ClaudeAgent()`, `config.ClaudeAgentYAML()` (Task 1); existing `sessionEnv(cfg, ws)` in `claude.go`.
- Produces:
  - `func proc.Shell() string`: `$SHELL` or `/bin/sh`. `Run` and `StartDaemon` use it.
  - `func parseSessionFlags(args []string) (noResume bool, rest []string, err error)`
  - `func quoteArg(s string) string`
  - `func resolveAgent(cfg *config.Config, stderr io.Writer) (*config.Agent, error)`
  - `const canResumeTimeout = 5 * time.Second`
  - `func probeCanResume(agent *config.Agent, dir string, env []string, timeout time.Duration) (bool, error)`
  - `func agentCommandLine(agent *config.Agent, resume bool, args []string) string`: the chosen command plus `" " + quoteArg(a)` per argument.

- [ ] **Step 1: Write the failing tests** (`session_test.go`)

- `TestParseSessionFlags` (table). Scanned only before the first `--`; the first `--` is dropped, everything after is appended verbatim.
  - `[]` → `false, nil, nil`
  - `["-R"]`, `["--no-resume"]` → `true, nil, nil`
  - `["--model", "opus"]` → `false, ["--model","opus"], nil`
  - `["-R", "--", "-S", "--claude-no-resume", "-R"]` → `true, ["-S","--claude-no-resume","-R"], nil`
  - `["-S"]`, `["--claude-no-skip-permissions"]` → `errors.Is(err, xerr.ErrUsage)`, message contains `skip-permissions is no longer injected; it is whatever your agent: commands say`
  - `["--claude-no-resume"]` → `ErrUsage`, message contains `use -R/--no-resume`
- `TestQuoteArg` (table, exact strings): `""` → `''`; `abc` → `'abc'`; `it's` → `'it'\''s'`; `a\b` → `'a'\\'b'`; `$HOME *` → `'$HOME *'`.
- `TestQuoteArgSurvivesShells`: for each of `sh`, `bash`, `zsh`, `fish` on PATH (skip missing): run `<shell> -c "printf '%s\0' " + quoted args` and compare the NUL-split output with the inputs `it's`, `back\slash`, `trail\`, `$HOME *`, `two\nlines`, `""`, `\'mix'\`.
- `TestResolveAgent`:
  - configured block → the same pointer's values, stderr empty;
  - no block, PATH = a dir holding an executable `claude` shim → `ClaudeAgent()` values, stderr exactly the fallback notice + `"\n"`;
  - no block, PATH = an empty dir → `errors.Is(err, xerr.ErrConfig)`, and the error text contains `string(config.ClaudeAgentYAML())`.
- `TestProbeCanResume` (`t.Setenv("SHELL", "/bin/sh")`):
  - `exit 0` → `true, nil`; `exit 1` → `false, nil`;
  - `echo boom >&2; exit 3` → error containing `exit 3` and `boom` and `-R`;
  - `sleep 30 & sleep 30` with timeout 300 ms → error containing `timed out`, and the call returns within 3 s (Review Focus 1);
  - stdout output does not appear anywhere in the error.
- `TestAgentCommandLine`: start vs resume chosen by the flag; args appended quoted; no args → the command unchanged.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/cli/ -run 'ParseSessionFlags|QuoteArg|ResolveAgent|ProbeCanResume|AgentCommandLine'`
Expected: compile errors.

- [ ] **Step 3: Implement**

- `quoteArg`: wrap in single quotes; emit each `'` as `'\''` and each `\` as `'\\'` (close quote, escaped char, reopen). This one scheme is correct in sh, bash, zsh and fish.
- `resolveAgent`: `cfg.Agent != nil` → return it. Else look up the first word of `config.ClaudeAgent().Start` with `exec.LookPath` (chaban's own PATH, see Rulings): found → write the notice line to `stderr`, return `config.ClaudeAgent()`; not found → `xerr.Wrap(xerr.ErrConfig, …)` whose message says there is no `agent:` block and no `claude` on PATH, then prints the built-in block as a sample to add to config.yml.
- `probeCanResume`: `exec.Command(proc.Shell(), "-lc", agent.CanResume)`, `Dir`, `Env` given, stdin nil, stdout discarded, stderr captured to a buffer. `SysProcAttr{Setpgid: true}`; on timeout kill `-pgid` with SIGKILL; `WaitDelay` 1 s. Error message names `can_resume`, how it failed (`exit N`, `timed out after 5s` with the actual duration, or the spawn error), the trimmed stderr, and that `-R` starts fresh without the check. Return a plain error (exit 1).
- `proc.Shell()`: move the two duplicated `$SHELL` lookups into it; no behavior change.

- [ ] **Step 4: Run and commit**

Run: `gofmt -l . && go vet ./... && go test ./...`

```bash
git add internal/cli/session.go internal/cli/session_test.go internal/proc
git commit -m "feat(cli): agent-neutral session building blocks"
```

---

### Task 3: `chaban chat`; `launch` and `try` on the new session path

**Files:**
- Create: `internal/cli/chat.go`, `internal/cli/chat_test.go`, `internal/cli/testdata/chat.txtar`
- Delete: `internal/cli/claude.go`'s Claude-specific code (`newClaudeCmd`'s body, `extractSessionFlags`, `buildClaudeArgv`, `hasConversation`, `conversationRecorded`, `encodeClaudeProjectDir`) and their tests; `internal/cli/testdata/claude.txtar`. Keep `sessionEnv`, `requireIdentFirst`, the signal/title handling: move them into `session.go`, then delete `claude.go` and `claude_test.go` (moving `TestSessionEnv` to `session_test.go`).
- Modify: `internal/cli/launch.go`, `internal/cli/try.go`, `internal/cli/root.go`, `internal/cli/completion.go`, `internal/cli/completion_test.go`, `internal/cli/testdata/{launch,try,usage_errors}.txtar`

**Interfaces:**
- Consumes: everything Task 2 produced.
- Produces:
  - `func runAgentSession(cfg *config.Config, ws wsp.Workspace, agent *config.Agent, noResume bool, args []string) error`: chooses start/resume (`noResume || agent.Resume == ""` → start; else `probeCanResume(agent, ws.Dir, sessionEnv(cfg, ws), canResumeTimeout)`, whose error aborts before anything starts), builds `agentCommandLine`, runs `proc.Shell() -lc <line>` in `ws.Dir` with inherited stdio and `sessionEnv`, keeps today's signal absorption and session titles, and passes the child's exit code through `xerr.Exit`.
  - `newChatCmd()`: `Use: "chat <workspace> [-R] [agent args...]"`, `Short: "Start or resume an agent session in the workspace"`, `DisableFlagParsing: true`, help handling and `requireIdentFirst("chat", "workspace", args)` as `claude` had.
  - `newClaudeRemovedCmd()`: `Use: "claude"`, `Hidden: true`, `DisableFlagParsing: true`, always `xerr.Wrap(xerr.ErrUsage, errors.New("`chaban claude` is now `chaban chat`"))`.

- [ ] **Step 1: Write the failing tests**

`chat.txtar` (replaces `claude.txtar`; set `env SHELL=/bin/sh` explicitly, shims first on PATH, HOME redirected). The config's `agent:` block points at an `agent` shim through the WORKROOT template: `start: <bin>/agent start`, `resume: <bin>/agent resume`, `can_resume: test -e <root>/can-resume`. The shim prints `argv: <args>` (space-joined after its first arg), its first arg as `mode: start|resume`, `cwd:`, and `WORKSPACE`, `PORT0`, one global env var.
- no `can-resume` file → `mode: start`, cwd is the workspace dir, overlay vars present;
- create the file → `mode: resume`;
- `-R` → `mode: start`;
- `chat T-1 -- "it's" '$HOME *'` → argv shows both verbatim;
- `chat T-1 -- -S --claude-no-resume` → passes through, `argv: -S --claude-no-resume` (Review Focus 3);
- `can_resume` replaced by `exit 2` (second config via `cp`) → no `mode:` line, stderr contains `can_resume failed`;
- `chat T-1 -S` → fails, stderr `no longer injected`;
- `chaban claude T-1` → fails, stderr ``is now `chaban chat` ``;
- fallback: config without `agent:`, a `claude` shim on PATH printing `argv: <args>` → stdout `argv: $` and stderr exactly the notice; then a `~/.claude/projects/<encoded>/c.jsonl` fixture (encode with `sed` in the script, independent of Go) → `argv: --continue$`.

`chat_test.go` (exact codes; `t.Setenv("SHELL", "/bin/sh")`):
- `TestChatExitCodePropagation`: agent `exit 7` → 7.
- `TestChatUsage`: `chat` with no args → 2; `chat -S T-1` → 2; `chat T-1 -S` → 2; `claude T-1 --model x` → 2.
- `TestChatNoAgentNoClaude`: no block, empty PATH dir → 4.
- `TestChatCanResumeFailureStartsNothing`: `can_resume: exit 3`, `start` touches a marker → exit 1, marker absent.
- `TestChatSessionSurvivesSIGINT`: port of `TestClaudeSessionSurvivesSIGINT`. With a shell in between, `$PPID` is no longer chaban: the test exports `CHABAN_TEST_PID=<os.Getpid()>` and the shim runs `kill -INT $CHABAN_TEST_PID`.
- `TestLaunchTryNoAgentNoClaudeCreateNothing`: no block, empty PATH → `launch T-9 desc` and `try some words` both exit 4; the registry file bytes and the root's directory listing are unchanged (Review Focus 5).

`launch.txtar`, `try.txtar`, `usage_errors.txtar`: rewrite their session assertions from injected Claude flags to the `agent` shim's `mode:`/`argv:` lines; `-S` cases become the usage error; `-- claude args` wording becomes `-- agent args`.

`completion_test.go`: `chat` completes the workspace slot exactly as `claude` did (move the existing cases); `claude` no longer completes.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/cli/`
Expected: failures in the new tests; compile errors until `chat.go` exists.

- [ ] **Step 3: Implement**

- `launch` and `try`: `parseSessionFlags` replaces `extractSessionFlags`; call `resolveAgent(cfg, cmd.ErrOrStderr())` **right after the config loads and before** any create/checkout; pass the agent to `runAgentSession`. Usage strings: `[-R] [-- agent args...]`; Long text says "agent session" instead of "Claude Code session".
- `root.go`: register `newChatCmd()` and `newClaudeRemovedCmd()`; remove `newClaudeCmd()`. Update the comment at line ~102 that names "the claude session".
- `completion.go`: key `"chat"`; comments say agent.

- [ ] **Step 4: Run and commit**

Run: `gofmt -l . && go vet ./... && go test -race ./internal/cli && go test ./...`

```bash
git add -A internal/cli
git commit -m "feat(cli)!: chaban chat replaces chaban claude"
```
Commit body: state which pins were mutation-checked (at least: the start/resume choice, the abort on probe failure, the fallback notice).

---

### Task 4: The instructions file follows config

**Files:**
- Modify: `internal/wsp/write.go` (+ its tests), `internal/cli/new.go:196`, `internal/cli/adopt.go:137`, `internal/cli/status.go`, `internal/cli/which.go`, `assets/hooks/session-start.sh`
- Test: `internal/wsp/write_test.go`, `internal/cli/status_test.go`, `internal/cli/which_test.go`, `internal/cli/testdata/{new,status_env,which_cd}.txtar` as needed, `internal/assets/assets_test.go` (hook)

**Interfaces:**
- Consumes: `(*config.Config).InstructionsFile()` (Task 1).
- Produces:
  - `func wsp.EnsureInstructionsFile(ws Workspace, name string) error` (replaces `EnsureClaudeMD`; same write-once O_EXCL semantics, same body).
  - `WriteWorkspaceMD` hint: `keep durable notes in <name> instead.`
  - `func recordedStatus(ws wsp.Workspace, file string) (status string, fileExists bool)`
  - `chaban which --instructions-file`: prints `filepath.Join(ws.Dir, cfg.InstructionsFile())`; `which --json` gains `"instructions_file": <same path>`.

- [ ] **Step 1: Write the failing tests**

- wsp: `EnsureInstructionsFile(ws, "AGENTS.md")` creates `AGENTS.md` with today's body and no `CLAUDE.md`; an existing file is left byte-for-byte. `WriteWorkspaceMD` with a config whose block sets `GEMINI.md` contains `keep durable notes in GEMINI.md instead.`
- new: with no `agent:` block a new workspace gets `CLAUDE.md`; with a block lacking `instructions_file` it gets `AGENTS.md`.
- status (human): section read from the configured file; label `status (from AGENTS.md):`; configured file missing → the line `no AGENTS.md in this workspace`; file present without a `## Status` section → no status block (as today). JSON unchanged.
- which: `--instructions-file` prints the absolute path (exists or not); `--json` carries `instructions_file`; outside a workspace still exit 3.
- hook (`assets_test.go`): the `chaban` shim answers `which --instructions-file` with a path to `AGENTS.md`; the nag and the closing handoff line both say `AGENTS.md`; when that call prints nothing, the nag is skipped and the handoff line says `the workspace's instructions file`; every path still exits 0.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/wsp/ ./internal/cli/ ./internal/assets/`

- [ ] **Step 3: Implement**

- Rename and thread the name; update the doc comments that say CLAUDE.md where they now mean "the instructions file" (keep the history notes that are about CLAUDE.md specifically).
- Hook: `file=$(chaban which --instructions-file 2>/dev/null)`, `fname=${file##*/}`; the nag greps `"$file"`; the two messages use `$fname`.
- `which` now needs the config: it already loads it via `loadRoot()`; keep that.

- [ ] **Step 4: Run and commit**

Run: `gofmt -l . && go vet ./... && go test ./...`

```bash
git add internal/wsp internal/cli assets/hooks internal/assets
git commit -m "feat!: the instructions file follows agent: instructions_file"
```

---

### Task 5: `doctor` reports the agent

**Files:**
- Modify: `internal/cli/doctor.go`
- Test: `internal/cli/testdata/doctor_*.txtar` or `doctor_test.go`, following the existing pattern

**Interfaces:**
- Consumes: `config.ClaudeAgent()`; the same first-word `exec.LookPath` rule as `resolveAgent` (factor a `fallbackProgram() string` in `session.go` if it helps; one rule, one place).

- [ ] **Step 1: Write the failing tests**

- No `agent:` block, `claude` shim on PATH → informational line containing `no agent: block: chat falls back to claude`; findings unchanged.
- No block, no `claude` → that note plus one finding: `no agent: block and no claude on PATH: chat cannot start a session`; summary counts it.
- A configured block → `printDoctorConfig` prints `agent: <start command>`; no note.
- `--json` carries the note and the finding with their kinds.

- [ ] **Step 2: Run them to see them fail**, **Step 3: Implement** (new kinds next to `kindDaemonNoDescription`), **Step 4: Run and commit**

```bash
git commit -m "feat(doctor): report the agent block and the claude fallback"
```

---

### Task 6: Docs

**Files:**
- Modify: `README.md`, `docs/reference.md`, `assets/skill/SKILL.md`, `CHANGELOG.md`, `CLAUDE.md` (project), `assets/config_stub.yml` (only if Task 1's header needs touching)

- [ ] **Step 1: README**: commands table (`chat <ws> [-R] [args…]`; `launch`/`try` flags `[-R] [-- args…]`), the session example, a short "Agents" section (the block, the fallback and its notice, `can_resume` exit codes, `instructions_file`), `status` wording ("the `## Status` note sessions keep in the workspace's instructions file"), and in the 2.0 upgrade checklist one step: copy the Claude sample into an `agent:` block (it keeps `instructions_file: CLAUDE.md`) and put `--dangerously-skip-permissions` there if wanted. README voice: short sentences, primary info, links out.
- [ ] **Step 2: reference**: replace the injection section (~lines 297-320) with the chat contract (start/resume choice, probe convention and timeout, argument quoting and the shells it is guaranteed for, `-R`, removed flags, `chaban claude`); the two-tier env paragraph says "agent session (`chat`, `launch`, `try`)"; exit codes: the shell's code passes through, a signal death under fish or a compound command shows as 128+n; Limits: argument passthrough is guaranteed for sh/bash/zsh/fish only; the instructions file section.
- [ ] **Step 3: SKILL.md**: `CLAUDE.md` → "the workspace's instructions file (WORKSPACE.md names it)"; any `-S` mention removed.
- [ ] **Step 4: CHANGELOG** under Unreleased, breaking: `chat` replaces `claude`; `-S` removed; skip-permissions no longer injected; `agent:` block; `instructions_file`; `which --instructions-file`; doctor lines; upgrade note.
- [ ] **Step 5: project CLAUDE.md**: the "Claude project-dir encoding" sharp edge now points at `internal/config/claude_agent.yml`; doc map lists the new spec; "`--` in session commands" stays true.
- [ ] **Step 6: Verify and commit**

Run: `grep -rn 'chaban claude\|-S\b\|claude-no-' README.md docs/reference.md assets/ CLAUDE.md`. Expected: only the removed-command/flag notes and CHANGELOG history.
Run: `go test ./internal/assets/` (stub/skill tests).

```bash
git commit -m "docs: agent-neutral sessions"
```
