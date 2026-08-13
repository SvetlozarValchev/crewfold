#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-scoped-mcp.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
fixture_root="$scenario_root/git-fixture"
daemon_log="$scenario_root/daemon.log"
daemon_pid=""

kill_owned_runtime_process() {
  pid=$1
  if [ -z "$pid" ] || [ "$pid" -le 1 ] || ! kill -0 "$pid" 2>/dev/null || [ ! -r "/proc/$pid/cmdline" ]
  then
    return
  fi
  command_line=$(tr '\000' ' ' <"/proc/$pid/cmdline")
  case "$command_line" in
    "$binary "*) kill -KILL "$pid" 2>/dev/null || true ;;
  esac
}

cleanup_runtime_processes() {
  for state_path in "$data_dir"/runtime/*/state.json
  do
    if [ ! -f "$state_path" ]
    then
      continue
    fi
    supervisor_pid=$(sed -n 's/.*"supervisor_pid":\([0-9][0-9]*\).*/\1/p' "$state_path")
    child_pid=$(sed -n 's/.*"child_pid":\([0-9][0-9]*\).*/\1/p' "$state_path")
    kill_owned_runtime_process "$supervisor_pid"
    kill_owned_runtime_process "$child_pid"
  done
}

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'scoped MCP acceptance failed; collected diagnostics follow\n' >&2
    for diagnostic in "$daemon_log" "$scenario_root/final.json" "$scenario_root/events.json" "$scenario_root/logs.json"
    do
      if [ -f "$diagnostic" ]
      then
        printf '%s\n' "$diagnostic" >&2
        sed -n '1,200p' "$diagnostic" >&2
      fi
    done
  fi
  if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    kill "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  cleanup_runtime_processes
  if [ -d "$scenario_root" ]
  then
    find "$scenario_root" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >/dev/null

start_daemon() {
  "$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" 2>>"$daemon_log" &
  daemon_pid=$!
  attempts=0
  while ! "$binary" status --socket "$socket_path" --output json >"$scenario_root/daemon-status.json" 2>/dev/null
  do
    if ! kill -0 "$daemon_pid" 2>/dev/null
    then
      wait "$daemon_pid" || true
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
  "$binary" daemon stop --socket "$socket_path" --output json >"$scenario_root/stop.json"
  wait "$daemon_pid"
  daemon_pid=""
}

extract_id() {
  prefix=$1
  path=$2
  sed -n "s/.*\"id\":\"\(${prefix}_[0-9a-f]*\)\".*/\1/p" "$path" | sed -n '1p'
}

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key scoped-mcp-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add demo --workspace personal --repo "$fixture_root/world-engine" --mode shared --socket "$socket_path" --idempotency-key scoped-mcp-project --output json >"$scenario_root/project.json"
"$binary" agent create scoped-worker --workspace personal --role implementer --provider fixture-mcp --runtime direct --socket "$socket_path" --idempotency-key scoped-mcp-agent --output json >"$scenario_root/agent.json"
"$binary" task create --workspace personal --project demo --title "Scoped MCP completion" --socket "$socket_path" --idempotency-key scoped-mcp-task --output json >"$scenario_root/task.json"
task_id=$(extract_id task "$scenario_root/task.json")
"$binary" task assign "$task_id" scoped-worker --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key scoped-mcp-assignment --output json >"$scenario_root/assigned.json"

"$binary" context build "$task_id" --workspace personal --agent scoped-worker --expected-task-revision 2 --socket "$socket_path" --idempotency-key scoped-mcp-context --output json >"$scenario_root/context.json"
context_id=$(extract_id ctx "$scenario_root/context.json")
if [ -z "$context_id" ]
then
  printf 'context packet ID is missing\n' >&2
  exit 1
fi
"$binary" context explain "$context_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/context-before.json"
grep -Fq '"accepted_knowledge"' "$scenario_root/context.json"
grep -Fq '"messages"' "$scenario_root/context.json"
grep -Fq '"transcripts"' "$scenario_root/context.json"

"$binary" run start "$task_id" --workspace personal --context "$context_id" --runtime direct --provider fixture-mcp --scenario "$repo_root/test/fixtures/scoped-mcp/completion.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key scoped-mcp-run --output json >"$scenario_root/run.json"
run_id=$(extract_id run "$scenario_root/run.json")
if [ -z "$run_id" ]
then
  printf 'run ID is missing\n' >&2
  exit 1
fi
grep -Fq "\"context_packet_id\":\"$context_id\"" "$scenario_root/run.json"

"$binary" run watch "$run_id" --workspace personal --wait-seconds 8 --socket "$socket_path" --output json >"$scenario_root/final.json"
grep -Fq '"status":"completed"' "$scenario_root/final.json"
grep -Fq '"step_cursor":2' "$scenario_root/final.json"
grep -Fq 'Review the immutable briefing' "$scenario_root/final.json"

"$binary" events list --after 0 --limit 200 --socket "$socket_path" --output json >"$scenario_root/events.json"
grep -Fq '"type":"run.tool_denied"' "$scenario_root/events.json"
grep -Fq '"type":"run.artifact_published"' "$scenario_root/events.json"
report_count=$(grep -o '"type":"run.report_received"' "$scenario_root/events.json" | wc -l)
if [ "$report_count" -ne 2 ]
then
  printf 'idempotent scoped reports produced %s durable report events, want 2\n' "$report_count" >&2
  exit 1
fi

"$binary" run logs "$run_id" --workspace personal --tail 100 --socket "$socket_path" --output json >"$scenario_root/logs.json"
grep -Fq 'CREWFOLD_MCP_CAPABILITY_FILE' "$scenario_root/logs.json"
grep -Fq 'CREWFOLD_MCP_SOCKET' "$scenario_root/logs.json"
if grep -Fq 'urn:crewfold:fixture-report' "$scenario_root/logs.json" || grep -Eq 'cf1\.run_[0-9a-f]{32}\.' "$scenario_root/logs.json"
then
  printf 'scoped runtime logs exposed structured reports or a capability token\n' >&2
  exit 1
fi

stop_daemon
start_daemon
"$binary" context explain "$context_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/context-after.json"
cmp "$scenario_root/context-before.json" "$scenario_root/context-after.json"
"$binary" run show "$run_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/restored.json"
grep -Fq '"status":"completed"' "$scenario_root/restored.json"
grep -Fq "\"context_packet_id\":\"$context_id\"" "$scenario_root/restored.json"
stop_daemon

printf 'Scoped MCP capability acceptance: PASS\n'
