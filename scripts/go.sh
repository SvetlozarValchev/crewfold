#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

if [ -n "${CREWFOLD_GO:-}" ]
then
  if [ ! -x "$CREWFOLD_GO" ]
  then
    printf 'CREWFOLD_GO is not executable: %s\n' "$CREWFOLD_GO" >&2
    exit 1
  fi
  exec "$CREWFOLD_GO" "$@"
fi

if command -v go >/dev/null 2>&1
then
  exec go "$@"
fi

toolchain_version=$(sed -n '1p' "$repo_root/.go-version")
data_home=${XDG_DATA_HOME:-"$HOME/.local/share"}
local_go="$data_home/crewfold-dev/toolchains/go$toolchain_version/bin/go"

if [ -x "$local_go" ]
then
  exec "$local_go" "$@"
fi

printf 'Go %s is required but no Go executable was found.\n' "$toolchain_version" >&2
printf 'Install it from https://go.dev/dl/ or set CREWFOLD_GO to the go executable.\n' >&2
exit 1
