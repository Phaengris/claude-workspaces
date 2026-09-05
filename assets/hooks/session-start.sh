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
# Workspaces created before the seeded frame (v1.8) have no ## Status section
# in CLAUDE.md; nag until a session starts one. Read-only: a grep, no writes.
# The pattern mirrors recordedStatus's predicate (case-insensitive, the
# trimmed line is exactly "## status") so the nag and the renderer cannot
# disagree about whether a note exists.
dir=$(workspace cd "$name" 2>/dev/null)
if [ -n "$dir" ] && ! grep -qi '^[[:space:]]*## status[[:space:]]*$' "$dir/CLAUDE.md" 2>/dev/null; then
	printf '\nThis workspace has no "## Status" section in its CLAUDE.md. Start one\n'
	printf '(About / Now / Next / Needs, with an as-of date) and refresh it at the end\n'
	printf 'of every substantial turn — the user reads it via `workspace status`.\n'
fi

printf '\nWORKSPACE.md holds the task, the allocated values and per-project instructions.\n'
printf 'Manage this workspace with: workspace status|up|down|logs|exec %s\n' "$name"
printf 'Daemons are not auto-started; start what you need with: workspace up %s <daemon>\n' "$name"

# The handoff-report convention, delivered here rather than seeded into the
# workspace's CLAUDE.md. CLAUDE.md is written once and then belongs to the
# agent (spec §5), so a convention seeded there is frozen at the workspace's
# birth and cannot be revised — while this hook stores nothing, is recomputed
# every session, and updates for every workspace at once the moment the binary
# does. Hence unconditional, unlike the nag above: the nag is about a section
# the AGENT owns and must start, this is a rule the TOOL owns and must be able
# to change. The skill carries the same rule at length for sessions that load
# it; this is the short form, and last in the block because it is the
# instruction that has to survive to the end of the turn.
printf '\nEnd every substantial turn with a handoff report — Done (outcomes, never the\n'
printf 'journey), Needs you (each ask self-contained: context, options and a\n'
printf 'recommendation in one breath), Watch out (problems found, with severity) — and\n'
printf 'refresh the ## Status section of CLAUDE.md in the same moment. Your LAST\n'
printf 'message is what the user reads when they switch back to this workspace; its\n'
printf 'job is re-entry in under a minute.\n'

exit 0
