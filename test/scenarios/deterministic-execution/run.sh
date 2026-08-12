#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
test_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-execution.XXXXXX")
binary="$test_root/crewfold"
data_dir="$test_root/data"
socket_path="$test_root/crewfold.sock"
fixture_root="$test_root/git-fixture"
daemon_log="$test_root/daemon.log"
daemon_pid=""

cleanup() {
  if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    kill "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  if [ -d "$test_root" ]
  then
    find "$test_root" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >/dev/null

start_daemon() {
  "$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" 2>>"$daemon_log" &
  daemon_pid=$!
  attempts=0
  while ! "$binary" status --socket "$socket_path" --output json >"$test_root/daemon-status.json" 2>/dev/null
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
  "$binary" daemon stop --socket "$socket_path" --output json >"$test_root/stop.json"
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
  "$binary" task create --workspace personal --project demo --title "$label" --socket "$socket_path" --idempotency-key "task-$key" --output json >"$test_root/task-$key.json"
  task_id=$(extract_id task "$test_root/task-$key.json")
  if [ -z "$task_id" ]
  then
    printf 'task ID missing for %s\n' "$key" >&2
    exit 1
  fi
  "$binary" task assign "$task_id" implementer --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key "assign-$key" --output json >"$test_root/assigned-$key.json"
  printf '%s\n' "$task_id"
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
    if [ "$attempts" -ge 200 ]
    then
      printf 'timed out waiting for run %s step %s\n' "$run_id" "$wanted_step" >&2
      sed -n '1,100p' "$output" >&2
      exit 1
    fi
    sleep 0.01
  done
}

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key execution-workspace --output json >"$test_root/workspace.json"
"$binary" project add demo --workspace personal --repo "$fixture_root/world-engine" --mode shared --socket "$socket_path" --idempotency-key execution-project --output json >"$test_root/project.json"
"$binary" checkout add demo "$fixture_root/world-engine-2" --workspace personal --mode exclusive --socket "$socket_path" --idempotency-key execution-adjacent --output json >"$test_root/adjacent.json"
adjacent_id=$(extract_id co "$test_root/adjacent.json")
if [ -z "$adjacent_id" ]
then
  printf 'adjacent checkout ID missing\n' >&2
  exit 1
fi
"$binary" agent create implementer --workspace personal --role implementer --provider fake --runtime fake --max-concurrency 10 --socket "$socket_path" --idempotency-key execution-agent --output json >"$test_root/agent.json"

success_task=$(create_assigned_task "Successful handoff" success)
"$binary" run start "$success_task" --workspace personal --checkout "$adjacent_id" --runtime fake --provider fake --scenario "$repo_root/test/fixtures/execution/success.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key run-success --output json >"$test_root/run-success.json"
success_run=$(extract_id run "$test_root/run-success.json")
"$binary" run watch "$success_run" --workspace personal --wait-seconds 5 --socket "$socket_path" --output json >"$test_root/success-final.json"
grep -Fq '"status":"completed"' "$test_root/success-final.json"
grep -Fq '"handoff"' "$test_root/success-final.json"
grep -Fq "\"checkout_path\":\"$fixture_root/world-engine-2\"" "$test_root/success-final.json"
grep -Fq '"checkout_kind":"standalone"' "$test_root/success-final.json"
"$binary" task timeline "$success_task" --workspace personal --socket "$socket_path" --output json >"$test_root/success-timeline.json"
grep -Fq '"kind":"run.completed"' "$test_root/success-timeline.json"
grep -Fq '"kind":"task.handoff_recorded"' "$test_root/success-timeline.json"

blocked_task=$(create_assigned_task "Blocked and resumed" blocked)
"$binary" run start "$blocked_task" --workspace personal --runtime fake --provider fake --scenario "$repo_root/test/fixtures/execution/blocked.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key run-blocked --output json >"$test_root/run-blocked.json"
blocked_run=$(extract_id run "$test_root/run-blocked.json")
"$binary" run watch "$blocked_run" --workspace personal --wait-seconds 5 --socket "$socket_path" >"$test_root/blocked-state.txt"
grep -Fq 'status: blocked' "$test_root/blocked-state.txt"
grep -Fq 'blocked: Which compatibility behavior should be preserved?' "$test_root/blocked-state.txt"
blocked_revision=$(sed -n 's/^revision: \([0-9][0-9]*\)$/\1/p' "$test_root/blocked-state.txt")
stop_daemon
start_daemon
"$binary" run show "$blocked_run" --workspace personal --socket "$socket_path" >"$test_root/blocked-restored.txt"
cmp "$test_root/blocked-state.txt" "$test_root/blocked-restored.txt"
"$binary" run resume "$blocked_run" --workspace personal --expected-revision "$blocked_revision" --socket "$socket_path" --idempotency-key resume-blocked --output json >"$test_root/resumed.json"
"$binary" run watch "$blocked_run" --workspace personal --wait-seconds 5 --socket "$socket_path" --output json >"$test_root/blocked-final.json"
grep -Fq '"status":"completed"' "$test_root/blocked-final.json"
grep -Fq '"step_cursor":3' "$test_root/blocked-final.json"

failure_task=$(create_assigned_task "Runtime start failure" start-failure)
"$binary" run start "$failure_task" --workspace personal --runtime fake --provider fake --scenario "$repo_root/test/fixtures/execution/start-failure.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key run-start-failure --output json >"$test_root/run-start-failure.json"
failure_run=$(extract_id run "$test_root/run-start-failure.json")
"$binary" run watch "$failure_run" --workspace personal --wait-seconds 5 --socket "$socket_path" --output json >"$test_root/start-failure-final.json"
grep -Fq '"status":"start_failed"' "$test_root/start-failure-final.json"
grep -Fq '"failure_code":"runtime_start_failed"' "$test_root/start-failure-final.json"
grep -Fq '"assignment_id":"asg_' "$test_root/start-failure-final.json"

review_task=$(create_assigned_task "Completion needs review" changes-requested)
"$binary" run start "$review_task" --workspace personal --runtime fake --provider fake --scenario "$repo_root/test/fixtures/execution/changes-requested.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key run-changes-requested --output json >"$test_root/run-changes-requested.json"
review_run=$(extract_id run "$test_root/run-changes-requested.json")
"$binary" run watch "$review_run" --workspace personal --wait-seconds 5 --socket "$socket_path" --output json >"$test_root/changes-requested-final.json"
grep -Fq '"status":"review"' "$test_root/changes-requested-final.json"
grep -Fq '"status":"changes_requested"' "$test_root/changes-requested-final.json"
grep -Fq 'missing security_reviewed' "$test_root/changes-requested-final.json"
if grep -Fq '"handoff":{' "$test_root/changes-requested-final.json"
then
  printf 'rejected completion unexpectedly created an accepted handoff\n' >&2
  exit 1
fi

restart_task=$(create_assigned_task "Resume after daemon restart" restart)
"$binary" run start "$restart_task" --workspace personal --runtime fake --provider fake --scenario "$repo_root/test/fixtures/execution/restart-checkpoint.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key run-restart --output json >"$test_root/run-restart.json"
restart_run=$(extract_id run "$test_root/run-restart.json")
wait_for_step "$restart_run" 1 "$test_root/restart-before.txt"
grep -Fq 'status: active' "$test_root/restart-before.txt"
restart_revision=$(sed -n 's/^revision: \([0-9][0-9]*\)$/\1/p' "$test_root/restart-before.txt")
stop_daemon
start_daemon
"$binary" run show "$restart_run" --workspace personal --socket "$socket_path" >"$test_root/restart-after.txt"
cmp "$test_root/restart-before.txt" "$test_root/restart-after.txt"
"$binary" run resume "$restart_run" --workspace personal --expected-revision "$restart_revision" --socket "$socket_path" --idempotency-key resume-restart --output json >"$test_root/restart-resumed.json"
"$binary" run watch "$restart_run" --workspace personal --wait-seconds 5 --socket "$socket_path" --output json >"$test_root/restart-final.json"
grep -Fq '"status":"completed"' "$test_root/restart-final.json"
grep -Fq '"step_cursor":2' "$test_root/restart-final.json"
grep -Fq '"summary":"The durable run resumed from its persisted cursor."' "$test_root/restart-final.json"

"$binary" run list --workspace personal --socket "$socket_path" --output json >"$test_root/runs.json"
grep -Fq "$success_run" "$test_root/runs.json"
grep -Fq "$blocked_run" "$test_root/runs.json"
grep -Fq "$failure_run" "$test_root/runs.json"
grep -Fq "$review_run" "$test_root/runs.json"
grep -Fq "$restart_run" "$test_root/runs.json"
stop_daemon

printf 'Deterministic execution acceptance: PASS\n'
