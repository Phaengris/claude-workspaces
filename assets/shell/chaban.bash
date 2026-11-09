# chaban shell wrapper (bash / zsh).
#
# `chaban cd <ws> [project]` PRINTS a directory — a child process cannot
# change its parent shell's working directory, so this function performs the
# chdir with the path the binary resolved. Every other subcommand is handed
# straight to the binary.
#
# `command chaban` is what makes that safe: it bypasses this function, so
# the wrapper cannot recurse into itself, and the binary keeps the terminal,
# stdin/stdout and its exit code (2 usage, 3 not found, 4 config — callers
# script against those).
#
# Installed by `chaban install` to
#   ~/.local/share/chaban/shell/chaban.bash
# SOURCE it from your shell's rc file — the installer does not edit rc files:
#
#   # ~/.bashrc or ~/.zshrc
#   . "$HOME/.local/share/chaban/shell/chaban.bash"

chaban() {
	local _ws_arg _ws_dir
	if [ "$1" = "cd" ]; then
		# `cd --help` prints a help page, not a path. `--json` is accepted and
		# deliberately ignored by `cd` today (a lone absolute path already IS
		# the machine-readable form), so it is reserved for a future JSON
		# shape; bypassing both here means a wrapper installed now keeps
		# working if that shape ever arrives.
		for _ws_arg in "$@"; do
			case "$_ws_arg" in
			--json | --help | -h)
				command chaban "$@"
				return $?
				;;
			esac
		done
		# Only chdir on success: a failed `cd` has already explained itself on
		# stderr and the shell must stay where it is, with the binary's exit
		# code intact. `local` is declared separately from the assignment on
		# purpose — `local x=$(cmd)` would report the status of `local`.
		_ws_dir=$(command chaban "$@") || return $?
		[ -n "$_ws_dir" ] || return 1
		cd "$_ws_dir" || return $?
		return 0
	fi
	command chaban "$@"
}
