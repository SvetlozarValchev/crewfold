#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-direct-runtime.XXXXXX")
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

create_assigned_task() {
  label=$1
  key=$2
  "$binary" task create --workspace personal --project demo --title "$label" --socket "$socket_path" --idempotency-key "task-$key" --output json >"$scenario_root/task-$key.json"
  task_id=$(extract_id task "$scenario_root/task-$key.json")
  if [ -z "$task_id" ]
  then
    printf 'task ID missing for %s\n' "$key" >&2
    exit 1
  fi
  "$binary" task assign "$task_id" direct-worker --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key "assign-$key" --output json >"$scenario_root/assigned-$key.json"
  printf '%s\n' "$task_id"
}

start_run() {
  task_id=$1
  fixture=$2
  key=$3
  "$binary" run start "$task_id" --workspace personal --runtime direct --provider fixture --scenario "$repo_root/test/fixtures/direct-runtime/$fixture.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key "run-$key" --output json >"$scenario_root/run-$key.json"
  run_id=$(extract_id run "$scenario_root/run-$key.json")
  if [ -z "$run_id" ]
  then
    printf 'run ID missing for %s\n' "$key" >&2
    exit 1
  fi
  printf '%s\n' "$run_id"
}

wait_for_step() {
  run_id=$1
  wanted_step=$2
  output=$3
  attempts=0
  while :
  do
    "$binary" run show "$run_id" --workspace personal --socket "$socket_path" >"$output"
    if grep -Fq "step: $wanted_step" "$output"
    then
      return
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 400 ]
    then
      printf 'timed out waiting for run %s step %s\n' "$run_id" "$wanted_step" >&2
      sed -n '1,100p' "$output" >&2
      exit 1
    fi
    sleep 0.01
  done
}

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key direct-runtime-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add demo --workspace personal --repo "$fixture_root/world-engine" --mode shared --socket "$socket_path" --idempotency-key direct-runtime-project --output json >"$scenario_root/project.json"
"$binary" agent create direct-worker --workspace personal --role implementer --provider fixture --runtime direct --max-concurrency 10 --socket "$socket_path" --idempotency-key direct-runtime-agent --output json >"$scenario_root/agent.json"

completion_task=$(create_assigned_task "Direct completion" completion)
completion_run=$(start_run "$completion_task" completion completion)
"$binary" run watch "$completion_run" --workspace personal --wait-seconds 8 --socket "$socket_path" --output json >"$scenario_root/completion-final.json"
grep -Fq '"status":"completed"' "$scenario_root/completion-final.json"
grep -Fq '"handoff"' "$scenario_root/completion-final.json"
"$binary" run logs "$completion_run" --workspace personal --tail 50 --socket "$socket_path" --output json >"$scenario_root/completion-logs.json"
grep -Fq '"state":"exited"' "$scenario_root/completion-logs.json"
grep -Fq "$fixture_root/world-engine" "$scenario_root/completion-logs.json"
if grep -Fq '\"HOME\"' "$scenario_root/completion-logs.json" || grep -Fq 'API_TOKEN' "$scenario_root/completion-logs.json"
then
  printf 'direct fixture received a non-allowlisted environment variable\n' >&2
  exit 1
fi

review_task=$(create_assigned_task "Direct completion needs review" changes-requested)
"$binary" run start "$review_task" --workspace personal --runtime direct --provider fixture --scenario "$repo_root/test/fixtures/execution/changes-requested.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key run-direct-changes-requested --output json >"$scenario_root/run-changes-requested.json"
review_run=$(extract_id run "$scenario_root/run-changes-requested.json")
"$binary" run watch "$review_run" --workspace personal --wait-seconds 8 --socket "$socket_path" --output json >"$scenario_root/changes-requested-final.json"
grep -Fq '"status":"review"' "$scenario_root/changes-requested-final.json"
grep -Fq '"status":"changes_requested"' "$scenario_root/changes-requested-final.json"
grep -Fq 'missing security_reviewed' "$scenario_root/changes-requested-final.json"
if grep -Fq '"handoff"' "$scenario_root/changes-requested-final.json"
then
  printf 'insufficient direct evidence unexpectedly created an accepted handoff\n' >&2
  exit 1
fi

if "$binary" run start "$completion_task" --workspace personal --runtime direct --provider fixture --scenario "$repo_root/test/fixtures/direct-runtime/completion.json" --working-directory "$fixture_root/world-engine-2" --expected-task-revision 2 --socket "$socket_path" >/dev/null 2>&1
then
  printf 'run start unexpectedly accepted a caller-supplied working directory\n' >&2
  exit 1
fi

output_task=$(create_assigned_task "Bounded process output" bounded-output)
output_run=$(start_run "$output_task" bounded-output bounded-output)
"$binary" run watch "$output_run" --workspace personal --wait-seconds 8 --socket "$socket_path" --output json >"$scenario_root/output-final.json"
"$binary" run logs "$output_run" --workspace personal --tail 5 --socket "$socket_path" --output json >"$scenario_root/output-logs.json"
truncated_count=$(grep -o '"truncated":true' "$scenario_root/output-logs.json" | wc -l)
if [ "$truncated_count" -ne 2 ] || ! grep -Eq '"omitted_bytes":[1-9][0-9]*' "$scenario_root/output-logs.json"
then
  printf 'bounded output did not report stdout and stderr truncation\n' >&2
  exit 1
fi

failure_task=$(create_assigned_task "Direct start failure" start-failure)
"$binary" run start "$failure_task" --workspace personal --runtime direct --provider fixture --scenario "$repo_root/test/fixtures/execution/start-failure.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key run-direct-start-failure --output json >"$scenario_root/run-start-failure.json"
failure_run=$(extract_id run "$scenario_root/run-start-failure.json")
"$binary" run watch "$failure_run" --workspace personal --wait-seconds 8 --socket "$socket_path" --output json >"$scenario_root/start-failure-final.json"
grep -Fq '"status":"start_failed"' "$scenario_root/start-failure-final.json"

crash_task=$(create_assigned_task "Direct process crash" crash)
crash_run=$(start_run "$crash_task" crash crash)
"$binary" run watch "$crash_run" --workspace personal --wait-seconds 8 --socket "$socket_path" --output json >"$scenario_root/crash-final.json"
grep -Fq '"failure_code":"process_exited"' "$scenario_root/crash-final.json"
grep -Fq 'code 17' "$scenario_root/crash-final.json"

timeout_task=$(create_assigned_task "Direct process timeout" timeout)
timeout_run=$(start_run "$timeout_task" timeout timeout)
"$binary" run watch "$timeout_run" --workspace personal --wait-seconds 8 --socket "$socket_path" --output json >"$scenario_root/timeout-final.json"
grep -Fq '"failure_code":"runtime_timeout"' "$scenario_root/timeout-final.json"

blocked_task=$(create_assigned_task "Blocked direct process" blocked)
blocked_run=$(start_run "$blocked_task" blocked blocked)
"$binary" run watch "$blocked_run" --workspace personal --wait-seconds 8 --socket "$socket_path" --output json >"$scenario_root/blocked.json"
grep -Fq '"status":"blocked"' "$scenario_root/blocked.json"
grep -Fq 'Which compatibility policy' "$scenario_root/blocked.json"
blocked_revision=$(sed -n 's/.*"run":{[^}]*"revision":\([0-9][0-9]*\).*/\1/p' "$scenario_root/blocked.json")
if [ -z "$blocked_revision" ]
then
  printf 'blocked run revision missing\n' >&2
  exit 1
fi
"$binary" run resume "$blocked_run" --workspace personal --expected-revision "$blocked_revision" --socket "$socket_path" --idempotency-key resume-direct-blocked --output json >"$scenario_root/resumed.json"
"$binary" run watch "$blocked_run" --workspace personal --wait-seconds 8 --socket "$socket_path" --output json >"$scenario_root/resumed-final.json"
grep -Fq '"status":"completed"' "$scenario_root/resumed-final.json"
grep -Fq 'fixture continued after the owner response' "$scenario_root/resumed-final.json"

graceful_task=$(create_assigned_task "Graceful process stop" graceful-stop)
graceful_run=$(start_run "$graceful_task" graceful-stop graceful-stop)
wait_for_step "$graceful_run" 1 "$scenario_root/graceful-before.txt"
graceful_revision=$(sed -n 's/^revision: \([0-9][0-9]*\)$/\1/p' "$scenario_root/graceful-before.txt")
"$binary" run stop "$graceful_run" --graceful --grace-millis 500 --workspace personal --expected-revision "$graceful_revision" --socket "$socket_path" --idempotency-key stop-graceful-fixture --output json >"$scenario_root/graceful-requested.json"
"$binary" run watch "$graceful_run" --workspace personal --wait-seconds 8 --socket "$socket_path" >"$scenario_root/graceful-final.txt"
grep -Fq 'status: stopped' "$scenario_root/graceful-final.txt"
grep -Fq 'stop_forced: false' "$scenario_root/graceful-final.txt"
"$binary" task show "$graceful_task" --workspace personal --socket "$socket_path" >"$scenario_root/graceful-task.txt"
grep -Fq 'status: assigned' "$scenario_root/graceful-task.txt"

stopped_task=$(create_assigned_task "Forced stop fallback" forced-stop)
stopped_run=$(start_run "$stopped_task" forced-stop forced-stop)
wait_for_step "$stopped_run" 1 "$scenario_root/stopping-before.txt"
stopped_revision=$(sed -n 's/^revision: \([0-9][0-9]*\)$/\1/p' "$scenario_root/stopping-before.txt")
"$binary" run stop "$stopped_run" --graceful --grace-millis 100 --workspace personal --expected-revision "$stopped_revision" --socket "$socket_path" --idempotency-key stop-forced-fixture --output json >"$scenario_root/stop-requested.json"
"$binary" run watch "$stopped_run" --workspace personal --wait-seconds 8 --socket "$socket_path" --output json >"$scenario_root/stopped-final.json"
grep -Fq '"status":"stopped"' "$scenario_root/stopped-final.json"
grep -Fq '"stop_forced":true' "$scenario_root/stopped-final.json"
grep -Fq '"status":"assigned"' "$scenario_root/stopped-final.json"

restart_task=$(create_assigned_task "Process survives daemon restart" restart)
restart_run=$(start_run "$restart_task" restart restart)
wait_for_step "$restart_run" 1 "$scenario_root/restart-before.txt"
grep -Fq 'status: active' "$scenario_root/restart-before.txt"
stop_daemon
start_daemon
"$binary" run watch "$restart_run" --workspace personal --wait-seconds 10 --socket "$socket_path" --output json >"$scenario_root/restart-final.json"
grep -Fq '"status":"completed"' "$scenario_root/restart-final.json"
grep -Fq 'child result reconciled after daemon restart' "$scenario_root/restart-final.json"
"$binary" run logs "$restart_run" --workspace personal --tail 50 --socket "$socket_path" --output json >"$scenario_root/restart-logs.json"
grep -Fq '"state":"exited"' "$scenario_root/restart-logs.json"

stop_daemon
printf 'Direct subprocess acceptance: PASS\n'
