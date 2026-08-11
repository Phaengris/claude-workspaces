# claude-workspaces (Go)

CLI tool (`workspace`) managing isolated dev workspaces for parallel Claude Code
sessions: a git worktree per project, an allocated port block, generated env,
and daemons — an *environment engine*. Clean-room Go rewrite (v1.0.x, complete,
daily-driven) of a retired Ruby tool; **never consult or port Ruby code** —
the spec and this repo are the only sources.

Module path is `git.internal/cat/claude-workspaces-go` (predates the repo rename —
renaming it is a deliberate deferral, don't "fix" casually; it touches every
import and the install ldflags).

## Commands

```
CGO_ENABLED=0 go build -o ./workspace ./cmd/workspace     # build (binary is gitignored)
go test ./...                                             # full suite (~11s; real processes, real git)
go test -race ./internal/proc ./internal/wsp ./internal/cli
gofmt -l . && go vet ./...                                # both must be clean before commit
# release: -ldflags "-X git.internal/cat/claude-workspaces-go/internal/cli.version=X.Y.Z", tag vX.Y.Z, ./workspace install
```

Conventional commits (`feat(cli): …`). The user's real install lives at
`~/.local/bin/workspace` — after user-facing fixes, rebuild with a bumped
version and run `./workspace install` (idempotent, manifest-driven).

## Architecture (dependency flow strictly downward)

```
cmd/workspace → internal/cli (one file per command; cobra)
  → internal/wsp    workspace domain: identity/Resolve, ensure-chain, writers,
                    daemon model, ResolveTargets grammar, CommandEnv (SOLE
                    spawn-env composer), runtime ${} substitution
  → internal/config strict YAML (goccy), template expansion on the raw tree,
                    validation (errors.Join, all-at-once)
  → internal/alloc  .allocations.json registry: flock + atomic write, index
                    gap-filling, values math (alloc.Block is the ONE formula home)
  → internal/proc   LEAF. $SHELL -lc spawn, daemons (Setpgid), pid+starttime
                    liveness, TERM→KILL StopGroup (leader-only)
  → internal/gitx   LEAF. argv-form git only; GIT_DIR-family env neutralized
  → internal/envx   LEAF. curated env: allowlist + sanitized PATH + env_allow
  → internal/xerr   exit-code kinds: 0/1/2 usage/3 not found/4 config; ExitError
                    carries a child's code verbatim (checked before kinds)
internal/assets ← assets/ (//go:embed carrier; skill, hook, wrappers, config stub)
```

## Load-bearing doctrines (violating these is a bug, not a style choice)

- **Derive, don't record**: the registry holds only allocations; everything
  else (checked out, setup-current, running) is derived live. No status field.
  Every op is an idempotent ensure; re-run converges. `new` is the one
  transaction (LIFO undo; halts at a failed worktree undo).
- **Two-tier env**: user commands (setup/start/stop/teardown/exec) get the
  CURATED env — `wsp.CommandEnv` → `envx.Curated`, exact-name allowlist +
  `env_allow` + sanitized PATH (concrete `/versions|installs/.../bin` stripped,
  shims kept). Claude sessions get sanitized-INHERITED env + workspace overlay.
  `proc.Run`/`StartDaemon` are the only user-command spawn paths; nil env means
  empty, never parent.
- **Destruction safety, layered**: config rejects escaping `path:`; every
  WorktreeRemove/RemoveAll is containment-gated (`isAncestorOrSame`, root
  Abs'd); adopted dirs are never deleted; teardown/stop failures abort before
  removal; gc -d's five gates (tool-created, ≥1 checked out, all merged via
  refs/heads-qualified IsMerged, no live pids-dir record, clean incl. Err).
  Doubt always reads as "keep".
- **pids directory is truth** for "what runs": down/restart(no-target)/destroy/
  gc/release/doctor enumerate `wsp.PidFileKeys`, not config keys. Liveness =
  kill-0 AND starttime match (zombies = dead). StopGroup refuses non-leaders;
  "stopped (TERM)" promises leader death only.
- **Completions never break the shell**: any error → nil + NoFileComp.
- Uninstall removes EXACTLY the manifest (survivors manifest on failures);
  install never edits settings.json/rc files — prints snippets.

## Testing conventions

- TDD; table tests for pure logic; testscript txtar for command flows
  (internal/cli/testdata/). Shared txtar helpers in cli_test.go: `wsenv`,
  `workroot` (WORKROOT/WORKDIR substitution + `! grep WORKROOT` guard).
- Real processes (bounded `sleep 30`, explicit `down` epilogues, one 5s
  KILL-escalation case per package) and real git repos (hermetic:
  GIT_CONFIG_GLOBAL/SYSTEM=/dev/null + author env). PATH shims for git/claude/
  xdg-open — a real `claude` must never be invocable from tests.
- Culture: mutation-check load-bearing pins (state it in the commit/PR);
  exact exit codes pinned in Go tests (txtar `! exec` is only "non-zero").
- testscript gotcha: `$VAR` expands in UNQUOTED chunks only — absolute-path
  asserts need `'…'$WORK'…'`.

## Known sharp edges (documented, don't rediscover)

- goccy rejects unquoted `${…}` inside flow-style YAML collections — block
  style in all examples.
- Claude project-dir encoding: EVERY non-alphanumeric byte → `-` (empirical;
  probe in claude.go). `${WORKSPACE}` = task id, NOT the dir name.
- `--` in session commands is the sniff-suppressor ONLY at the slot after the
  workspace; later `--` reaches the child verbatim.
- v1 divergences are consolidated in README's appendix (biggest: `stop:` runs
  AFTER daemons stop).

## Doc map

- `docs/superpowers/specs/2026-07-30-claude-workspaces-go-design.md` — THE
  spec; decided behaviors live here and in each milestone plan's
  Decided-behaviors table (`docs/superpowers/plans/*-m[0-5]-*.md`).
- `docs/superpowers/plans/2026-08-10-m5-deferred-items.md` — the post-v1.0
  backlog + standing decisions (do not re-litigate without cause).
- `README.md` — user-facing; its claims are accuracy-audited against the code.
  If behavior changes, the README changes in the same commit.
