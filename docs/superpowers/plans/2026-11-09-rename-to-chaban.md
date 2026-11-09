# Rename claude-workspaces → chaban Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The tool stops carrying "claude" in its name: product, repo, Go module, binary, root env var, default root and skill all become *chaban*.

**Architecture:** A rename, done in layers that each leave the suite green: module path, then root resolution, then the command name, then the installed layout, then the docs. No behavior changes beyond the name, the new default root, and two guards that stop an upgraded install from silently starting over in an empty root. Released as **v2.0.0** (breaking: binary name, env var, default root).

**Tech Stack:** Go, cobra, testscript (txtar), embedded assets.

**Spec:** this plan. The decisions were made in conversation on 2026-11-09 and are listed under Decided behaviors. The design spec (`docs/superpowers/specs/2026-07-30-claude-workspaces-go-design.md`) keeps governing everything this plan doesn't change.

## Decided behaviors

| # | Decision |
|---|---|
| 1 | Product, repo and module: `chaban`, module path `github.com/Phaengris/chaban`. |
| 2 | Binary and cobra root command: `chaban`. No short alias ships; users add their own (`abbr ws chaban`). |
| 3 | Root env var: `CHABAN_ROOT_DIR`. `CLAUDE_WORKSPACES_ROOT_DIR` is no longer read (see guard 5). |
| 4 | Default root: `~/workspaces`. An existing `~/claude-workspaces` is NOT moved; its owner points `CHABAN_ROOT_DIR` at it. |
| 5 | Guard: `CHABAN_ROOT_DIR` unset and `CLAUDE_WORKSPACES_ROOT_DIR` set → config error (exit 4) telling the user to rename the variable. |
| 6 | Guard: no override, `~/workspaces` missing and `~/claude-workspaces` exists → config error (exit 4) telling the user to set `CHABAN_ROOT_DIR` or move the folder. This stops `install` from writing a fresh stub into a new empty root. |
| 7 | "workspace" the NOUN stays everywhere (`WORKSPACE.md`, `${WORKSPACE}`, "workspace X created", help text about workspaces). Only "workspace" as the PROGRAM name changes (`workspace up` → `chaban up`, `command workspace`, the `workspace:` stderr prefix). |
| 8 | Historical docs (`docs/superpowers/specs/*`, `docs/superpowers/plans/*` before this one, `.remember/`) are not edited: they're dated history. |
| 9 | Upgrade path: run the OLD `workspace uninstall` first (it removes exactly its own manifest), then `chaban install`. No migration code in chaban. |
| 10 | The `claude` and `launch` subcommands keep their names: they describe what they launch. |

## Global Constraints

- Public repo: no infra hostnames, no Fizzy URLs in any file this plan touches.
- `gofmt -l .` empty and `go vet ./...` clean before every commit.
- Conventional commits; one commit per task; push only in Task 6.
- No new dependencies.
- **No pattern substitution on the word `workspace`, ever**: it is the tool's core noun and a substring of names, paths and prose. Program-name uses change by reading each hit (decision 7). `sed` is allowed only for the two exact tokens `github.com/Phaengris/claude-workspaces/` and `CLAUDE_WORKSPACES_ROOT_DIR`, over the files the task lists, and for the anchored whole-line fix below.

## Review Focus

1. A user upgrades without reading the CHANGELOG and with no env var: guard 6 must fire before `install` creates `~/workspaces` (Task 2 pins it, Task 4 pins that `install` surfaces it as exit 4 and creates nothing).
2. A user had `CLAUDE_WORKSPACES_ROOT_DIR` set: guard 5 fires, and it fires even if `~/workspaces` exists (Task 2).
3. Both variables set: `CHABAN_ROOT_DIR` wins silently, no guard (Task 2).
4. A leftover `workspace` program-name mention in a user-facing string sends a session to a command that no longer exists. Task 3's grep gate catches it.
5. A completion run in a root that trips a guard must stay silent (no suggestions, no error). The existing broken-root completion test gets a guard case (Task 2).

---

### Task 1: Module path

**Files:**
- Modify: `go.mod`, every `*.go` importing `github.com/Phaengris/claude-workspaces/...`
- Modify: `CLAUDE.md` (the `-ldflags -X github.com/Phaengris/claude-workspaces/internal/cli.version` lines)

**Interfaces:** Produces: module `github.com/Phaengris/chaban`; package paths unchanged below it.

- [ ] **Step 1:** `go mod edit -module github.com/Phaengris/chaban`, then rewrite imports: `git ls-files '*.go' | xargs sed -i 's|github.com/Phaengris/claude-workspaces/|github.com/Phaengris/chaban/|g'`. Same substitution in `CLAUDE.md`'s ldflags lines.
- [ ] **Step 2:** Verify. `gofmt -l .` (goimports grouping may reorder, so fix any output), `go vet ./...`, `go test ./...` all green; `git grep -n 'Phaengris/claude-workspaces' -- '*.go' go.mod CLAUDE.md` prints nothing.
- [ ] **Step 3:** Commit `refactor: module path github.com/Phaengris/chaban`.

### Task 2: Root resolution

**Files:**
- Modify: `internal/config/load.go` (`RootDir`), `internal/config/load_test.go`
- Modify: every test that sets the root variable: `internal/cli/cli_test.go:56`, `install_test.go`, `ls_test.go`, and the `env CLAUDE_WORKSPACES_ROOT_DIR=` lines in `internal/cli/testdata/*.txtar`
- Modify: comments naming the old variable in `internal/cli/install.go`, `internal/cli/uninstall.go`
- Test: `internal/config/load_test.go`, `internal/cli/completion_test.go` (`TestCompletionsSurviveBrokenRoot`)

**Interfaces:** Produces `config.RootDir() (string, error)`, same signature; errors from the guards are `xerr.Wrap(xerr.ErrConfig, …)` so every caller already maps them to exit 4.

- [ ] **Step 1: Write the failing table test** `TestRootDir` in `load_test.go`, each case with its own `t.TempDir()` as `HOME` and both variables set via `t.Setenv` (empty string = unset; `RootDir` treats empty as unset, as it does today):

| case | CHABAN_ROOT_DIR | CLAUDE_WORKSPACES_ROOT_DIR | dirs in HOME | want |
|---|---|---|---|---|
| override | `/custom/root` | – | – | `/custom/root` |
| override beats legacy var | `/custom/root` | `/old` | `claude-workspaces` | `/custom/root` |
| default | – | – | – | `$HOME/workspaces` |
| default exists, legacy dir too | – | – | `workspaces`, `claude-workspaces` | `$HOME/workspaces` |
| legacy var | – | `/old` | – | error, `errors.Is(err, xerr.ErrConfig)`, message contains `CLAUDE_WORKSPACES_ROOT_DIR is no longer read: rename it to CHABAN_ROOT_DIR` |
| legacy var, new default exists | – | `/old` | `workspaces` | same error as above |
| legacy dir only | – | – | `claude-workspaces` | error, ErrConfig, message contains `the default root is now ~/workspaces: set CHABAN_ROOT_DIR=` and the absolute legacy path |

Replace the two existing `RootDir` cases at `load_test.go:162-170`.

- [ ] **Step 2:** `go test ./internal/config -run TestRootDir` fails.
- [ ] **Step 3: Implement** `RootDir` in `load.go`. Exact error copy:
  - `CLAUDE_WORKSPACES_ROOT_DIR is no longer read: rename it to CHABAN_ROOT_DIR`
  - `%s exists, but the default root is now ~/workspaces: set CHABAN_ROOT_DIR=%s to keep using it, or move it` (both `%s` = the absolute legacy path)
  
  The legacy-dir check is an `os.Stat` on both paths; any Stat error other than not-exist on `~/workspaces` counts as "exists" (doubt reads as keep: no guard, no new root). Doc comment names both guards and why they exist (install writes a stub into a missing root).
- [ ] **Step 4:** Rename the variable in every test and txtar listed above (`sed -i 's/CLAUDE_WORKSPACES_ROOT_DIR/CHABAN_ROOT_DIR/g'` over those files, NOT over `load.go` or the new test). Fix `testdata/install.txtar:8`'s comment.
- [ ] **Step 5: Add the completion guard case** to `TestCompletionsSurviveBrokenRoot`: `CHABAN_ROOT_DIR` unset, `CLAUDE_WORKSPACES_ROOT_DIR` set → `cd ""` yields no suggestions, directive NoFileComp, no error.
- [ ] **Step 6:** `go test ./...` green. Mutation-check: delete each guard in turn and confirm its case fails; note it in the commit.
- [ ] **Step 7:** Commit `feat(config)!: CHABAN_ROOT_DIR, default root ~/workspaces`.

### Task 3: Command name

**Files:**
- Move: `cmd/workspace/` → `cmd/chaban/`
- Modify: `internal/cli/root.go` (`Use: "chaban"`, the `"workspace:"` stderr prefix at :109 → `"chaban:"`), `internal/xerr/xerr.go:38` comment
- Modify: every user-facing Go string naming the program, e.g. `internal/wsp/write.go:70,152-153`, `internal/cli/claude.go:94` (`"workspace %s requires …"` → `"chaban %s requires …"`), cobra `Example:`/`Long:` texts
- Modify: `internal/cli/cli_test.go:14-21` (testscript command `"chaban"`), all `internal/cli/testdata/*.txtar` and `internal/wsp/testdata/*` (`exec workspace` → `exec chaban`, expected `workspace:` stderr → `chaban:`, expected hint strings)
- Modify: `.gitignore` (`/workspace` → `/chaban`), `CLAUDE.md` build commands (`-o ./chaban ./cmd/chaban`, `./chaban install`)

**Interfaces:** Consumes Task 1's module path. Produces the binary `chaban`, built from `./cmd/chaban`.

- [ ] **Step 1: Update the tests first:** testscript registration and every txtar `exec`/stderr/stdout expectation; Go tests asserting program-name strings. Apply decision 7 by reading each hit, not by blind sed: `doctor_test.go:166`'s `"workspace"` is a JSON key (noun), it stays.
- [ ] **Step 2:** `go test ./internal/...` fails on the renamed expectations.
- [ ] **Step 3:** Move `cmd/workspace` to `cmd/chaban` (`git mv`), change `root.go`, the stderr prefix and the strings found in Step 1.
- [ ] **Step 4: Grep gate.** This prints only noun uses (review each line; list the survivors in the commit message):

  ```bash
  git grep -nE '(`|"|command |exec )workspace( |$|`)' -- internal cmd assets
  ```

  `go test ./...`, `gofmt`, `vet` green.
- [ ] **Step 5:** Commit `feat(cli)!: the command is chaban`.

### Task 4: Installed layout and assets

**Files:**
- Move: `assets/shell/workspace.fish` → `assets/shell/chaban.fish`, `assets/shell/workspace.bash` → `assets/shell/chaban.bash`; update `internal/assets/assets.go` embeds
- Modify: `assets/shell/*` (function name `chaban`, `command chaban`, header comments), `assets/hooks/session-start.sh` (`command -v chaban`, `chaban which`, `chaban status`, header line `# chaban`), `assets/skill/SKILL.md` (`name: chaban`, title, every command, root default `~/workspaces` and `CHABAN_ROOT_DIR`), `assets/config_stub.yml` header
- Modify: `internal/cli/install.go:61-70` layout, the printed next-steps snippets (:198-235), `internal/cli/uninstall.go`
- Test: `internal/cli/install_test.go`, `internal/cli/testdata/install.txtar`, `internal/assets/assets_test.go`

**Interfaces:** Consumes Task 3's binary name. Produces this layout (install manifest records exactly these):

| asset | path |
|---|---|
| binary | `~/.local/bin/chaban` |
| share dir | `~/.local/share/chaban/` (manifest `install-manifest.json`, `hooks/session-start.sh`, `shell/chaban.bash`, `completions/chaban.bash`, `completions/_chaban`) |
| fish wrapper | `~/.config/fish/functions/chaban.fish` |
| fish completions | `~/.config/fish/completions/chaban.fish` |
| skill | `~/.claude/skills/chaban/SKILL.md` |

- [ ] **Step 1: Update the tests first** to the layout table, the skill's `name: chaban`, the hook header `# chaban`, and the printed snippets. Add one install case: a guard-6 home (`claude-workspaces` dir, no env vars) → `install` exits 4 and `~/workspaces` does not exist afterwards.
- [ ] **Step 2:** Tests fail.
- [ ] **Step 3:** Implement: rename the asset files and embeds, the layout, the snippet text, the asset contents.
- [ ] **Step 4:** `go test ./...` green. Real check in a scratch HOME:

  ```bash
  H=$(mktemp -d)
  HOME=$H CHABAN_ROOT_DIR=$H/r ./chaban install
  HOME=$H fish --no-config -c "source $H/.config/fish/functions/chaban.fish; functions chaban"   # prints the wrapper
  HOME=$H CHABAN_ROOT_DIR=$H/r ./chaban uninstall && find $H -mindepth 1 | grep -v "^$H/r"     # prints nothing
  ```
- [ ] **Step 5:** Commit `feat(install)!: chaban layout, skill and wrappers`.

### Task 5: Docs

**Files:**
- Modify: `README.md` (title, every command, install paths, root default, env var, new sections below), `docs/reference.md`, `examples/rails.md`, `CLAUDE.md` (title line, prose naming the tool, root default), `CHANGELOG.md`

- [ ] **Step 1:** Rename across the public docs with decision 7 applied by reading. Keep the "not affiliated with Anthropic" footer.
- [ ] **Step 2:** Add to README, right after the opening section, this exact copy (the user may edit it in review):

  > ## Why "chaban"
  >
  > *Chaban* means shepherd. The word came from Persian *chupan* through Turkic *çoban*, and lives on in Russian and Ukrainian *чабан* and Romanian *cioban*. A shepherd keeps one flock where every animal has its own pen. chaban keeps your parallel Claude Code sessions the same way: each workspace gets its own worktree, ports and daemons. The tool was called claude-workspaces before v2.0.

- [ ] **Step 3:** Add a README section "Upgrading from claude-workspaces" with, in order: `workspace uninstall` (the old binary); `set -Ux CHABAN_ROOT_DIR ~/claude-workspaces` (fish) / `export CHABAN_ROOT_DIR=…` (bash/zsh) to keep the old root (before installing: guard 6 stops `chaban install` otherwise); `chaban install`; re-apply the printed settings.json snippets (hook path and statusline command changed); existing workspaces' `CLAUDE.md` files are write-once and carry the seeded line `every substantial turn. The user reads it from outside via `workspace status`,`. The fix replaces that exact whole line and nothing else (it matches no prose a session wrote). Show it as a preview-then-apply pair: `grep -lx '<line>' <root>/*/CLAUDE.md` lists the files; then `sed -i.bak 's/^every substantial turn\. The user reads it from outside via `workspace status`,$/every substantial turn. The user reads it from outside via `chaban status`,/' <those files>`. The `.bak` copies stay until the user deletes them.
- [ ] **Step 4:** CHANGELOG Unreleased gets a `### Changed (breaking)` block: the rename, the env var, the default root, the two guards, a pointer to the upgrade section.
- [ ] **Step 5:** Accuracy pass: `git grep -n -i 'claude-workspaces\|CLAUDE_WORKSPACES' -- README.md docs/reference.md examples CLAUDE.md assets internal` prints only the intentional mentions (the history sentence, the upgrade section, guard copy and its tests). Commit `docs: chaban`.

### Task 6: Repo, release and the user's machine (with the user, not a subagent)

- [ ] Rename the GitHub repo (`gh repo rename chaban` from the checkout; GitHub redirects the old URL) and the private mirror; update both remote URLs.
- [ ] Rename the Fizzy board `claude-workspaces` → `chaban` (user's call).
- [ ] Release v2.0.0 per CLAUDE.md: retitle Unreleased, build with `version=2.0.0`, old `workspace uninstall`, `./chaban install`, tag `v2.0.0`, push master and tag to both remotes.
- [ ] User's machine: `set -Ux CHABAN_ROOT_DIR ~/claude-workspaces` (BEFORE `chaban install`, or guard 6 stops it); `abbr ws chaban`; settings.json snippets; the anchored CLAUDE.md line fix (on 2026-11-09 it matched 87 of 105 workspaces, and every `workspace status` mention there is that seeded line; the four other program-name mentions are session-written history and stay).
- [ ] Optional: rename the local checkout `~/dev/claude-workspaces` → `~/dev/chaban`. Claude Code keys this project's memory by that path: move its `~/.claude/projects/<encoded path>/` directory to the new path's encoding in the same step.
