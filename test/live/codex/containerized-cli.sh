#!/usr/bin/env bash
set -euo pipefail

image=${CREWFOLD_CODEX_CONTAINER_IMAGE:-crewfold-codex-live:local}
host_home=$(getent passwd "$(id -u)" | cut -d: -f6)
if [[ -z "$host_home" || ! -d "$host_home" ]]
then
  printf 'containerized Codex: cannot resolve the current user home\n' >&2
  exit 1
fi
source_codex_home=${CODEX_HOME:-$host_home/.codex}
container_codex_home=$(mktemp -d "$host_home/.crewfold-codex-home.XXXXXX")

cleanup() {
  status=$?
  if [[ -d "$container_codex_home" ]]
  then
    find "$container_codex_home" -depth -delete
  fi
  exit "$status"
}
trap cleanup EXIT HUP INT TERM

if [[ ! -f "$source_codex_home/auth.json" ]]
then
  printf 'containerized Codex: auth.json is missing from %s\n' "$source_codex_home" >&2
  exit 1
fi
install -m 0600 "$source_codex_home/auth.json" "$container_codex_home/auth.json"
if [[ -f "$source_codex_home/installation_id" ]]
then
  install -m 0600 "$source_codex_home/installation_id" "$container_codex_home/installation_id"
fi

if ! docker image inspect "$image" >/dev/null 2>&1
then
  printf 'containerized Codex: image is not local: %s (pull and inspect it first)\n' "$image" >&2
  exit 1
fi

docker_args=(
  run --rm --pull never --read-only
  --cap-drop ALL --pids-limit 512
  --user "$(id -u):$(id -g)"
  --tmpfs /tmp:rw,nosuid,nodev,size=256m
  --env "HOME=/tmp"
  --env "CODEX_HOME=$container_codex_home"
  --mount "type=bind,src=$container_codex_home,dst=$container_codex_home"
)

socket_path=${CREWFOLD_MCP_SOCKET:-}
capability_path=${CREWFOLD_MCP_CAPABILITY_FILE:-}
if [[ -n "$socket_path" || -n "$capability_path" ]]
then
  if [[ -z "$socket_path" || -z "$capability_path" ]]
  then
    printf 'containerized Codex: both MCP socket and capability file are required\n' >&2
    exit 1
  fi
  socket_path=$(realpath -e -- "$socket_path")
  scope_root=$(dirname -- "$socket_path")
  capability_path=$(realpath -e -- "$capability_path")
  case "$capability_path" in
    "$scope_root"/*) ;;
    *)
      printf 'containerized Codex: MCP capability is outside the disposable scope\n' >&2
      exit 1
      ;;
  esac
  docker_args+=(
    --env "CREWFOLD_MCP_SOCKET=$socket_path"
    --env "CREWFOLD_MCP_CAPABILITY_FILE=$capability_path"
    --mount "type=bind,src=$scope_root,dst=$scope_root"
  )
fi

docker "${docker_args[@]}" "$image" /usr/local/bin/codex "$@"
