#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
image=${CREWFOLD_CLAUDE_CONTAINER_IMAGE:-crewfold-claude-live:local}
host_claude=${CREWFOLD_HOST_CLAUDE_BINARY:-$(command -v claude)}
build_root=$(mktemp -d "$script_dir/.container-build.XXXXXX")

cleanup() {
  status=$?
  if [[ -d "$build_root" ]]
  then
    find "$build_root" -depth -delete
  fi
  exit "$status"
}
trap cleanup EXIT HUP INT TERM

if [[ ! -x "$host_claude" ]]
then
  printf 'containerized Claude build: host binary is not executable: %s\n' "$host_claude" >&2
  exit 1
fi
host_claude=$(realpath -e -- "$host_claude")

cp "$script_dir/Containerfile" "$build_root/Containerfile"
cp "$host_claude" "$build_root/claude"
docker build --pull=false -t "$image" -f "$build_root/Containerfile" "$build_root"
