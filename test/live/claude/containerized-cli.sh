#!/usr/bin/env bash
set -euo pipefail

image=${CREWFOLD_CLAUDE_CONTAINER_IMAGE:-crewfold-claude-live:local}
host_home=$(getent passwd "$(id -u)" | cut -d: -f6)
if [[ -z "$host_home" || ! -d "$host_home" ]]
then
  printf 'containerized Claude: cannot resolve the current user home\n' >&2
  exit 1
fi
source_config_dir=${CREWFOLD_HOST_CLAUDE_CONFIG_DIR:-${CLAUDE_CONFIG_DIR:-$host_home/.claude}}
temporary_config_dir=$(mktemp -d "$host_home/.crewfold-claude-config.XXXXXX")
container_config_dir=/var/lib/crewfold-claude

cleanup() {
  status=$?
  if [[ -d "$temporary_config_dir" ]]
  then
    find "$temporary_config_dir" -depth -delete
  fi
  exit "$status"
}
trap cleanup EXIT HUP INT TERM

if [[ ! -f "$source_config_dir/.credentials.json" ]]
then
  printf 'containerized Claude: .credentials.json is missing from %s\n' "$source_config_dir" >&2
  exit 1
fi
install -m 0600 "$source_config_dir/.credentials.json" "$temporary_config_dir/.credentials.json"

if ! docker image inspect "$image" >/dev/null 2>&1
then
  printf 'containerized Claude: image is not local: %s (build and inspect it first)\n' "$image" >&2
  exit 1
fi

docker_args=(
  run --rm --pull never --read-only
  --cap-drop ALL --pids-limit 512
  --user "$(id -u):$(id -g)"
  --tmpfs /tmp:rw,nosuid,nodev,size=256m
  --env "HOME=/tmp"
  --env "CLAUDE_CONFIG_DIR=$container_config_dir"
  --mount "type=bind,src=$temporary_config_dir,dst=$container_config_dir"
)
container_workdir=/tmp

socket_path=${CREWFOLD_MCP_SOCKET:-}
capability_path=${CREWFOLD_MCP_CAPABILITY_FILE:-}
if [[ -n "$socket_path" || -n "$capability_path" ]]
then
  if [[ -z "$socket_path" || -z "$capability_path" ]]
  then
    printf 'containerized Claude: both MCP socket and capability file are required\n' >&2
    exit 1
  fi
  socket_path=$(realpath -e -- "$socket_path")
  scope_root=$(dirname -- "$socket_path")
  capability_path=$(realpath -e -- "$capability_path")
  container_workdir=$(realpath -e -- "$PWD")
  case "$capability_path" in
    "$scope_root"/*) ;;
    *)
      printf 'containerized Claude: MCP capability is outside the disposable scope\n' >&2
      exit 1
      ;;
  esac
  case "$container_workdir" in
    "$scope_root"|"$scope_root"/*) ;;
    *)
      printf 'containerized Claude: working directory is outside the disposable scope\n' >&2
      exit 1
      ;;
  esac
  docker_args+=(
    --env "CREWFOLD_MCP_SOCKET=$socket_path"
    --env "CREWFOLD_MCP_CAPABILITY_FILE=$capability_path"
    --mount "type=bind,src=$scope_root,dst=$scope_root"
  )
fi

docker "${docker_args[@]}" --workdir "$container_workdir" "$image" /usr/local/bin/claude "$@"
