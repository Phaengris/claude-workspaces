# Changelog

All notable changes to `workspace` are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

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

[1.3.0]: https://github.com/Phaengris/claude-workspaces/compare/v1.2.0...v1.3.0
[1.2.0]: https://github.com/Phaengris/claude-workspaces/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/Phaengris/claude-workspaces/compare/v1.0.2...v1.1.0
[1.0.2]: https://github.com/Phaengris/claude-workspaces/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/Phaengris/claude-workspaces/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/Phaengris/claude-workspaces/releases/tag/v1.0.0
