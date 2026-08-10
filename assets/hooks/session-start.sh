#!/bin/sh
# SessionStart hook for claude-workspaces.
#
# When a Claude Code session starts inside a managed workspace, print a short
# context block: which workspace this is, and its live status. Outside a
# workspace, print nothing.
#
# Read-only and unconditionally successful. It creates nothing, writes
# nothing and starts nothing (spec §8: the hook adds context, it never
# mutates), and every path through it exits 0 — a hook that failed would
# surface as a session-start error, and "you are not in a workspace" is not an
# error.
#
# Installed by `workspace install` to
#   ~/.local/share/workspace/hooks/session-start.sh
# and wired up by adding the snippet `install` prints to
# ~/.claude/settings.json. The installer never edits that file itself.

# The binary may be absent: uninstalled, or not on PATH in the environment
# Claude Code runs hooks in. Nothing to say either way.
command -v workspace >/dev/null 2>&1 || exit 0

# `workspace which` IS the "am I inside a workspace?" test: the workspace name
# on stdout, or a non-zero exit (3 = not inside a workspace, 4 = broken
# config, …). Any failure means "no context to add" — including the broken
# config, which the user will see the moment they run a real command and does
# not need shouted at session start. Its diagnostic is discarded for the same
# reason.
name=$(workspace which 2>/dev/null) || exit 0
[ -n "$name" ] || exit 0

# `status` output is passed through verbatim rather than reformatted: it is
# the tool's own rendering of derived state (projects, branches, setup
# freshness, running daemons), and a second formatting of the same facts here
# would be one more thing to keep in step.
printf '# claude-workspaces\n\n'
printf 'This session is inside workspace %s.\n\n' "$name"
workspace status "$name" 2>/dev/null
printf '\nWORKSPACE.md holds the task, the allocated values and per-project instructions.\n'
printf 'Manage this workspace with: workspace status|up|down|logs|exec %s\n' "$name"

exit 0
