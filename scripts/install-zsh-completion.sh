#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 3 ]; then
  printf 'usage: %s <wb-binary> <completion-dir> <zshrc>\n' "$0" >&2
  exit 2
fi

wb_binary="$1"
completion_dir="$2"
zshrc="$3"
completion_file="$completion_dir/_wb"
begin_marker='# >>> workbench completion >>>'
end_marker='# <<< workbench completion <<<'

mkdir -p "$completion_dir"
completion_tmp="$(mktemp "$completion_dir/.wb-completion.XXXXXX")"
cleanup() {
  rm -f "$completion_tmp"
}
trap cleanup EXIT

"$wb_binary" completion zsh >"$completion_tmp"
chmod 0644 "$completion_tmp"
mv "$completion_tmp" "$completion_file"
trap - EXIT

mkdir -p "$(dirname "$zshrc")"
touch "$zshrc"
if ! grep -Fqx "$begin_marker" "$zshrc"; then
  quoted_dir="${completion_dir//\'/\'\\\'\'}"
  {
    printf '\n%s\n' "$begin_marker"
    printf "if [[ -d '%s' ]]; then\n" "$quoted_dir"
    printf "  fpath=('%s' \$fpath)\n" "$quoted_dir"
    printf 'fi\n'
    printf 'autoload -Uz compinit\n'
    printf 'if (( ! $+functions[compdef] )); then\n'
    printf '  compinit\n'
    printf 'fi\n'
    printf 'autoload -Uz _wb\n'
    printf 'compdef _wb wb\n'
    printf '%s\n' "$end_marker"
  } >>"$zshrc"
fi

printf 'installed zsh completion %s\n' "$completion_file"
printf 'registered Workbench completion in %s\n' "$zshrc"
