#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)

if [ "${CREWFOLD_LIVE_HERDR:-}" != "1" ]
then
  printf 'Installed Herdr conformance: SKIP (set CREWFOLD_LIVE_HERDR=1)\n'
  exit 0
fi

if ! command -v herdr >/dev/null 2>&1
then
  printf 'Installed Herdr conformance: SKIP (herdr is not installed)\n'
  exit 0
fi

session="crewfold-live-$$"
live_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-live-herdr.XXXXXX")
server_log="$live_root/herdr-server.log"
server_pid=''

cleanup() {
  status=$?
  HERDR_SESSION="$session" herdr server stop >/dev/null 2>&1 || true
  if [ -n "$server_pid" ] && kill -0 "$server_pid" 2>/dev/null
  then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  herdr session delete "$session" >/dev/null 2>&1 || true
  if [ "$status" -ne 0 ] && [ -f "$server_log" ]
  then
    sed -n '1,200p' "$server_log" >&2
  fi
  find "$live_root" -depth -delete
}
trap cleanup EXIT HUP INT TERM

HERDR_SESSION="$session" herdr server >"$server_log" 2>&1 &
server_pid=$!
attempts=0
until HERDR_SESSION="$session" herdr api snapshot >/dev/null 2>&1
do
  if ! kill -0 "$server_pid" 2>/dev/null
  then
    wait "$server_pid" || true
    printf 'dedicated Herdr server exited during startup\n' >&2
    exit 1
  fi
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 200 ]
  then
    printf 'timed out waiting for the dedicated Herdr session\n' >&2
    exit 1
  fi
  sleep 0.025
done

export CREWFOLD_HERDR_BINARY=$(command -v herdr)
export CREWFOLD_HERDR_SESSION="$session"
export CREWFOLD_ACCEPTANCE_RUNTIME=herdr
export CREWFOLD_ACCEPTANCE_PROVIDER=fixture-terminal
export CREWFOLD_ACCEPTANCE_WAKE=succeeded
export CREWFOLD_ACCEPTANCE_ATTACH=false

"$repo_root/test/scenarios/agent-messaging/run.sh"
printf 'Installed Herdr conformance: PASS\n'
