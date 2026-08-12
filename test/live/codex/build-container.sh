#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
image=${CREWFOLD_CODEX_CONTAINER_IMAGE:-crewfold-codex-live:local}
host_codex=${CREWFOLD_HOST_CODEX_BINARY:-$(command -v codex)}
host_code_mode=${CREWFOLD_HOST_CODE_MODE_BINARY:-$(dirname "$host_codex")/codex-code-mode-host}
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

if [[ ! -x "$host_codex" ]]
then
  printf 'containerized Codex build: host binary is not executable: %s\n' "$host_codex" >&2
  exit 1
fi
if [[ ! -x "$host_code_mode" ]]
then
  printf 'containerized Codex build: code-mode host is not executable: %s\n' "$host_code_mode" >&2
  exit 1
fi

cp "$script_dir/Containerfile" "$build_root/Containerfile"
cp "$host_codex" "$build_root/codex"
cp "$host_code_mode" "$build_root/codex-code-mode-host"
docker build --pull=false -t "$image" -f "$build_root/Containerfile" "$build_root"
