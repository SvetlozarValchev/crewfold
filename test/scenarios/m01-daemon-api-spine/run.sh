#!/bin/sh
set -eu

scenario_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$scenario_dir/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-m01.XXXXXX")
daemon_pid=

cleanup() {
  if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    kill -TERM "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  if [ -n "$temp_dir" ] && [ -d "$temp_dir" ]
  then
    find "$temp_dir" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

cd "$repo_root"
export GOTOOLCHAIN=local
export GOPROXY=off

binary="$temp_dir/crewfold"
$go_runner build -trimpath -o "$binary" ./cmd/crewfold

data_dir="$temp_dir/data"
socket_path="$temp_dir/crewfold.sock"
daemon_log="$temp_dir/daemon.log"

start_daemon() {
  "$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" \
    >"$temp_dir/daemon.stdout" 2>>"$daemon_log" &
  daemon_pid=$!
}

wait_ready() {
  attempts=0
  while [ "$attempts" -lt 200 ]
  do
    if "$binary" status --socket "$socket_path" --output json >"$temp_dir/status.json" 2>"$temp_dir/status.err"
    then
      return 0
    fi
    if ! kill -0 "$daemon_pid" 2>/dev/null
    then
      printf 'daemon exited before readiness\n' >&2
      sed -n '1,120p' "$daemon_log" >&2
      return 1
    fi
    attempts=$((attempts + 1))
    sleep 0.01
  done
  printf 'timed out waiting for daemon readiness\n' >&2
  return 1
}

start_daemon
wait_ready

grep -Fq '"schema":"urn:crewfold:schema:local-api:status-result:v1"' "$temp_dir/status.json"
grep -Fq '"status":"ok"' "$temp_dir/status.json"
grep -Fq '"protocol":1' "$temp_dir/status.json"

socket_mode=$(stat -c '%a' "$socket_path")
if [ "$socket_mode" != "600" ]
then
  printf 'socket permissions = %s, want 600\n' "$socket_mode" >&2
  exit 1
fi

set +e
"$binary" daemon run \
  --data-dir "$temp_dir/other-data" \
  --socket "$socket_path" \
  --output json \
  >"$temp_dir/live-socket.stdout" 2>"$temp_dir/live-socket.stderr"
live_socket_exit=$?
set -e
if [ "$live_socket_exit" -ne 1 ]
then
  printf 'live socket collision exit = %s, want 1\n' "$live_socket_exit" >&2
  exit 1
fi
grep -Fq '"code":"socket_in_use"' "$temp_dir/live-socket.stderr"

set +e
"$binary" daemon run \
  --data-dir "$data_dir" \
  --socket "$temp_dir/other.sock" \
  --output json \
  >"$temp_dir/live-data.stdout" 2>"$temp_dir/live-data.stderr"
live_data_exit=$?
set -e
if [ "$live_data_exit" -ne 1 ]
then
  printf 'live data-directory collision exit = %s, want 1\n' "$live_data_exit" >&2
  exit 1
fi
grep -Fq '"code":"data_dir_in_use"' "$temp_dir/live-data.stderr"
if [ -e "$temp_dir/other.sock" ]
then
  printf 'data-directory collision unexpectedly created another socket\n' >&2
  exit 1
fi

grep -Fq '"msg":"daemon started"' "$daemon_log"
grep -Fq '"method":"system.status"' "$daemon_log"
grep -Fq '"request_id":"req-' "$daemon_log"

# Abruptly terminate only the daemon owned by this scenario. Unix releases its
# file lock but leaves the socket path because the process cannot run cleanup.
kill -KILL "$daemon_pid"
set +e
wait "$daemon_pid" 2>/dev/null
set -e
daemon_pid=
if [ ! -S "$socket_path" ]
then
  printf 'fault injection did not leave a stale socket\n' >&2
  exit 1
fi

start_daemon
wait_ready
grep -Fq '"status":"ok"' "$temp_dir/status.json"

"$binary" daemon stop --socket "$socket_path" --output json >"$temp_dir/stop.json"
grep -Fq '"schema":"urn:crewfold:schema:local-api:stop-result:v1"' "$temp_dir/stop.json"
grep -Fq '"status":"stopping"' "$temp_dir/stop.json"
wait "$daemon_pid"
daemon_pid=

if [ -e "$socket_path" ]
then
  printf 'socket still exists after graceful shutdown\n' >&2
  exit 1
fi

grep -Fq '"msg":"daemon stopped"' "$daemon_log"
printf 'M1 acceptance: PASS\n'
