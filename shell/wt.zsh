# wt — worktree dashboard. The binary cannot change this shell's directory,
# so it writes the selection to $TMPDIR/wt-cd and this function acts on it.
#
# NOTE: the chosen-directory variable below is deliberately named
# "wt_target", not "path". zsh ties the lowercase "path" array to $PATH, and
# `local path` inside a function creates a *local, empty* tied copy of PATH
# for the rest of that call -- silently breaking lookup of every external
# command (including `claude`) until the function returns. Verified: with
# `local path`, `claude --resume "$session"` below fails with "command not
# found: claude" even though `claude` is on the caller's real PATH.
wt() {
  local choice_file="${TMPDIR:-/tmp}/wt-cd"
  rm -f "$choice_file"

  command wt "$@" || return $?

  [[ -f "$choice_file" ]] || return 0

  local line wt_target session
  line="$(<"$choice_file")"
  rm -f "$choice_file"
  wt_target="${line%%$'\t'*}"
  session="${line#*$'\t'}"
  [[ "$session" == "$line" ]] && session=""

  [[ -d "$wt_target" ]] || return 0
  cd "$wt_target" || return 1

  if [[ -n "$session" ]]; then
    claude --resume "$session"
  fi
}
