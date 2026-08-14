#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-persistence.XXXXXX")
binary="$temp_dir/crewfold"
data_dir="$temp_dir/data"
socket_path="$temp_dir/crewfold.sock"
daemon_log="$temp_dir/daemon.log"
daemon_pid=""

cleanup() {
  if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    kill "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  if [ -d "$temp_dir" ]
  then
    find "$temp_dir" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"

start_daemon() {
  "$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" 2>>"$daemon_log" &
  daemon_pid=$!

  attempts=0
  while ! "$binary" status --socket "$socket_path" --output json >"$temp_dir/status.json" 2>/dev/null
  do
    if ! kill -0 "$daemon_pid" 2>/dev/null
    then
      wait "$daemon_pid" || true
      printf 'daemon exited before readiness\n' >&2
      sed -n '1,200p' "$daemon_log" >&2
      exit 1
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 200 ]
    then
      printf 'timed out waiting for daemon readiness\n' >&2
      exit 1
    fi
    sleep 0.01
  done
}

stop_daemon() {
  "$binary" daemon stop --socket "$socket_path" --output json >"$temp_dir/stop.json"
  wait "$daemon_pid"
  daemon_pid=""
  if [ -e "$socket_path" ]
  then
    printf 'socket remained after graceful stop: %s\n' "$socket_path" >&2
    exit 1
  fi
}

start_daemon

"$binary" doctor --database --socket "$socket_path" --output json >"$temp_dir/database.json"
grep -Fq '"schema":"urn:crewfold:schema:local-api:database-status-result:v1"' "$temp_dir/database.json"
grep -Fq '"status":"ok"' "$temp_dir/database.json"
schema_version=$(sed -n 's/.*"schema_version":\([0-9][0-9]*\).*/\1/p' "$temp_dir/database.json")
if [ "$schema_version" != "1" ]
then
  printf 'schema version = %s, want the sole current baseline 1\n' "$schema_version" >&2
  exit 1
fi
grep -Eq '"baseline_sha256":"[0-9a-f]{64}"' "$temp_dir/database.json"
grep -Eq '"catalog_sha256":"[0-9a-f]{64}"' "$temp_dir/database.json"
grep -Fq '"journal_mode":"wal"' "$temp_dir/database.json"
grep -Fq '"foreign_keys":true' "$temp_dir/database.json"
grep -Fq '"integrity_check":"ok"' "$temp_dir/database.json"

database_mode=$(stat -c '%a' "$data_dir/crewfold.db")
if [ "$database_mode" != "600" ]
then
  printf 'database mode = %s, want 600\n' "$database_mode" >&2
  exit 1
fi

"$binary" workspace init personal \
  --socket "$socket_path" \
  --idempotency-key initialize-personal \
  --output json >"$temp_dir/init.json"
grep -Fq '"schema":"urn:crewfold:schema:local-api:workspace-init-result:v1"' "$temp_dir/init.json"
grep -Fq '"name":"personal"' "$temp_dir/init.json"
grep -Fq '"revision":1' "$temp_dir/init.json"
grep -Fq '"event_sequence":1' "$temp_dir/init.json"

"$binary" workspace init personal \
  --socket "$socket_path" \
  --idempotency-key initialize-personal \
  --output json >"$temp_dir/replay.json"
cmp "$temp_dir/init.json" "$temp_dir/replay.json"

if "$binary" workspace init personal \
  --socket "$socket_path" \
  --idempotency-key another-command \
  --output json >"$temp_dir/duplicate.out" 2>"$temp_dir/duplicate.err"
then
  printf 'duplicate workspace unexpectedly succeeded\n' >&2
  exit 1
fi
grep -Fq '"code":"workspace_already_exists"' "$temp_dir/duplicate.err"

"$binary" workspace show personal --socket "$socket_path" --output json >"$temp_dir/show-before.json"
workspace_id=$(sed -n 's/.*"id":"\(ws_[0-9a-f]*\)".*/\1/p' "$temp_dir/show-before.json")
if [ -z "$workspace_id" ]
then
  printf 'could not read workspace ID from result\n' >&2
  exit 1
fi

"$binary" events list --workspace personal --socket "$socket_path" --after 0 --output json >"$temp_dir/events.json"
grep -Fq '"schema":"urn:crewfold:schema:local-api:events-list-result:v1"' "$temp_dir/events.json"
grep -Fq '"workspace_id":"'"$workspace_id"'"' "$temp_dir/events.json"
grep -Fq '"high_water":1' "$temp_dir/events.json"
grep -Fq '"next_cursor":""' "$temp_dir/events.json"
grep -Fq '"has_more":false' "$temp_dir/events.json"
grep -Fq '"total":1' "$temp_dir/events.json"
grep -Fq '"type":"workspace.created"' "$temp_dir/events.json"
event_count=$(grep -o '"event_id":"evt_[0-9a-f]*"' "$temp_dir/events.json" | wc -l)
if [ "$event_count" -ne 1 ]
then
  printf 'event count = %s, want 1\n' "$event_count" >&2
  exit 1
fi

"$binary" events list --workspace personal --socket "$socket_path" --after 1 --output json >"$temp_dir/events-after.json"
grep -Fq '"events":[]' "$temp_dir/events-after.json"

stop_daemon
start_daemon

"$binary" workspace show "$workspace_id" --socket "$socket_path" --output json >"$temp_dir/show-after.json"
cmp "$temp_dir/show-before.json" "$temp_dir/show-after.json"
"$binary" workspace init personal \
  --socket "$socket_path" \
  --idempotency-key initialize-personal \
  --output json >"$temp_dir/replay-after-restart.json"
cmp "$temp_dir/init.json" "$temp_dir/replay-after-restart.json"
"$binary" events list --workspace personal --socket "$socket_path" --after 0 --output json >"$temp_dir/events-after-restart.json"
cmp "$temp_dir/events.json" "$temp_dir/events-after-restart.json"

stop_daemon
printf 'Persistent workspace acceptance: PASS\n'
