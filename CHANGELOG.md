# Changelog

All notable changes to `workspace` are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [1.5.0] — 2026-08-14

### Added

- **`ls -a/--all`**: also lists root directories no allocation claims —
  released workspaces (the tool's `.workspace` footprint identifies them;
  task id derived from the dir name; `adopt` to reuse) and stranger dirs,
  labeled `(unmanaged)`. The registry stays allocations-only: "archived" is
  a condition of the world, derived on demand, never a recorded status.
- `doctor` notes every unregistered dir in the root (uncounted) — the
  always-on discoverability net for the same state.

## [1.4.0] — 2026-08-14

### Changed

- **Worktree branches are named after the full workspace name**
  (`PATED_patternima-editor-fixes`), not the bare task id — the branch now
  says in `git branch` and in a PR list what it is about. Applies to new
  checkouts; existing workspaces keep their task-id branches and every
  command keeps working with them (branches are always read from the
  worktree, never recomputed).
- `gc --destroy-dirs`'s merged gate now checks each project's **actual**
  branch instead of recomputing it from the task id — which also makes the
  gate honest for adopted workspaces and hand-switched worktrees, and reads
  an unreadable branch as "keep".

## [1.3.6] — 2026-08-14

### Added

- `workspace ls` (and the no-argument `status` listing) print column
  headers: `WORKSPACE  INDEX  TASK  DESCRIPTION`, plus `PROJECTS` under
  `-g`. The empty listing keeps its plain "no workspaces" line. `--json`
  is unchanged.

## [1.3.5] — 2026-08-14

### Changed

- `browse` dials the port before opening: nothing listening is a refusal
  that names the `workspace up` to run (and hands over the URL), instead of
  a browser tab pointing at a dead port. The socket is asked directly — a
  hand-started server counts, daemon records are not consulted. The skill
  teaches the sequence: start what serves, then browse or verify.

## [1.3.4] — 2026-08-14

### Added

- `doctor` reports a finding for a `browse_port` whose `${…}` template does
  not resolve to a port number — the broken-config case load validation
  cannot see (token resolution needs the values math). Complements 1.3.3:
  the typo class is now caught at load, at doctor, and at browse.

## [1.3.3] — 2026-08-14

### Fixed

- `browse` no longer opens impossible URLs. A `browse_port` that is neither
  a number nor a `${VALUE}` template is now a config error at load with a
  did-you-mean hint (`browse_port: PORT0` → *did you mean "${PORT0}"?* —
  the observed real-use typo opened `http://localhost:PORT0`); a template
  whose token fails to resolve is a loud error at browse time instead of a
  browser tab full of garbage.

## [1.3.2] — 2026-08-14

### Changed

- Workspace completion offers full names only. Task ids still resolve when
  typed, but completing both listed every workspace twice — and an id is
  always a prefix of its name, so a typed id-prefix reaches the full name
  (which, unlike a bare task number, says what the workspace is about).

## [1.3.1] — 2026-08-12

### Changed

- Docs/history hygiene: internal host names scrubbed from the repository
  and its history (module-proxy consumers should use this version or later).
  README gains the "Separation, not virtualization" section and the
  changelog itself. No code changes.

## [1.3.0] — 2026-08-12

### Added

- **Live progress for the ensure chain.** `new`, `checkout`, `up` and
  `launch` now report each slow step as it runs — the worktree checkout and
  every `setup:` command — one line each, completed in place with its
  duration (`  app: setup: npm ci… ok (11.6s)`). Idempotent re-runs stay as
  quiet as before: only work that actually runs reports. Plain text, no
  spinners, pipe-safe; setup output itself stays captured.
- **`launch` tips the parallel terminal.** On the create path, launch now
  prints `tip: in another terminal: workspace cd <id> — work alongside this
  session` instead of the `hint: workspace cd` line, which read as a pending
  to-do right before Claude took over the terminal. `new` keeps the hint
  (there it *is* the next step); launch's reuse path prints neither.

## [1.2.0] — 2026-08-12

### Added

- **Automatic session titles.** `claude` and `launch` sessions title the
  terminal (OSC escape, when stdout is a terminal, first 40 characters of
  the workspace name) and rename the current tmux window (`tmux
  rename-window`, first 20, when inside tmux). When the session ends, the
  window's `automatic-rename` option is unset — the exact undo of the
  rename — so tmux auto-naming resumes on default configs and manual-name
  configs stay untouched. Best-effort and silent: no tmux, no tty, no
  problem. (The return of v1's `title` command, automated — the first
  dropped command daily use proved missed.)
- MIT license; GitHub Actions CI (full suite + race detector on Linux,
  build + vet on macOS); README badges, `go install` instructions and a
  "How this was built" section.

## [1.1.0] — 2026-08-11

### Changed

- **Daemons are lazy.** `launch` no longer starts daemons (and no longer
  restarts dead ones when reusing a workspace). Start what you need with
  `workspace up <ws> [target…]` — the session-start status block tells the
  session what exists. **Breaking** relative to 1.0 behavior.

### Added

- **Per-daemon `description:`.** `start:` entries accept a third shape,
  `{name: {command, description}}`. Descriptions render — with `${}` values
  substituted — in `workspace status` (human and JSON), in WORKSPACE.md's
  new per-project services list, and reach every Claude session through the
  SessionStart hook, so a session knows what each daemon is for before
  starting it.
- `workspace doctor` notes (uncounted, never a finding) every configured
  daemon without a description.
- The skill and the SessionStart hook teach the lazy-daemons convention.

## [1.0.2] — 2026-08-11

### Changed

- Module path renamed to `github.com/Phaengris/claude-workspaces` (the
  public home); the installed skill directory sheds its `-go` suffix.

## [1.0.1] — 2026-08-11

### Fixed

- Shell completion for `workspace launch`: project names now complete after
  the description slot (and workspace identifiers in the first slot when
  flags precede it).

## [1.0.0] — 2026-08-10

Initial release: the complete environment engine, a clean-room Go rewrite
of an earlier personal Ruby tool (never publicly released).

- Workspaces: `new`, `checkout`, `destroy`, `adopt`, `release`, `gc` — a
  git worktree per project, an allocated index deriving ports and other
  values, generated `.env`, WORKSPACE.md/CLAUDE.md scaffolding. Derived
  state throughout: the only registry is the allocations file.
- Services: `up`, `down`, `restart`, `logs` with `project:daemon` targets,
  pid/starttime liveness, TERM→KILL escalation, run-and-wait preludes and
  `stop:` epilogues.
- Sessions: `claude` and `launch` with flag injection, history-aware
  `--continue`, `--` passthrough, curated-vs-inherited two-tier environment.
- Introspection: `ls`, `status`, `ports`, `env`, `which`, `cd`, `browse`,
  `doctor`; meaningful exit codes (0/1/2/3/4).
- Install: `install`/`uninstall` (manifest-driven), embedded Claude Code
  skill, SessionStart hook, shell wrappers and completions for
  fish/bash/zsh.

[1.5.0]: https://github.com/Phaengris/claude-workspaces/compare/v1.4.0...v1.5.0
[1.4.0]: https://github.com/Phaengris/claude-workspaces/compare/v1.3.6...v1.4.0
[1.3.6]: https://github.com/Phaengris/claude-workspaces/compare/v1.3.5...v1.3.6
[1.3.5]: https://github.com/Phaengris/claude-workspaces/compare/v1.3.4...v1.3.5
[1.3.4]: https://github.com/Phaengris/claude-workspaces/compare/v1.3.3...v1.3.4
[1.3.3]: https://github.com/Phaengris/claude-workspaces/compare/v1.3.2...v1.3.3
[1.3.2]: https://github.com/Phaengris/claude-workspaces/compare/v1.3.1...v1.3.2
[1.3.1]: https://github.com/Phaengris/claude-workspaces/compare/v1.3.0...v1.3.1
[1.3.0]: https://github.com/Phaengris/claude-workspaces/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/Phaengris/claude-workspaces/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/Phaengris/claude-workspaces/compare/v1.0.2...v1.1.0
[1.0.2]: https://github.com/Phaengris/claude-workspaces/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/Phaengris/claude-workspaces/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/Phaengris/claude-workspaces/releases/tag/v1.0.0
