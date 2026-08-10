# claude-workspaces shell wrapper (fish).
#
# `workspace cd <ws> [project]` PRINTS a directory — a child process cannot
# change its parent shell's working directory, so this function performs the
# chdir with the path the binary resolved. Every other subcommand is handed
# straight to the binary.
#
# `command workspace` is what makes that safe: it bypasses this function, so
# the wrapper cannot recurse into itself, and the binary keeps the terminal,
# stdin/stdout and its exit code (2 usage, 3 not found, 4 config — callers
# script against those).
#
# Installed by `workspace install` to
#   ~/.config/fish/functions/workspace.fish
# where fish autoloads it on first use; no sourcing and no shell restart.

function workspace --description 'claude-workspaces (with a working `cd`)'
    if test (count $argv) -gt 0; and test "$argv[1]" = cd
        # `cd --help` prints a help page, not a path. `--json` is accepted and
        # deliberately ignored by `cd` today (a lone absolute path already IS
        # the machine-readable form), so it is reserved for a future JSON
        # shape; bypassing both here means a wrapper installed now keeps
        # working if that shape ever arrives.
        for arg in $argv
            switch $arg
                case --json --help -h
                    command workspace $argv
                    return $status
            end
        end
        # Only chdir on success: a failed `cd` has already explained itself on
        # stderr and the shell must stay where it is, with the binary's exit
        # code intact. (`set` reports the command substitution's status —
        # fish 3.4+. On an older fish the status would be lost, but then `dir`
        # is empty and the guard below still refuses to move.)
        set -l dir (command workspace $argv)
        set -l rc $status
        if test $rc -ne 0
            return $rc
        end
        if test -z "$dir"
            return 1
        end
        cd "$dir"
        return $status
    end
    command workspace $argv
end
