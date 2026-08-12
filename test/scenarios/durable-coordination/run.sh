#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-coordination.XXXXXX")
binary="$temp_dir/crewfold"
data_dir="$temp_dir/data"
socket_path="$temp_dir/crewfold.sock"
fixture_root="$temp_dir/git-fixture"
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
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >"$temp_dir/fixture-paths.txt"

start_daemon() {
  "$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" 2>>"$daemon_log" &
  daemon_pid=$!
  attempts=0
  while ! "$binary" status --socket "$socket_path" --output json >"$temp_dir/daemon-status.json" 2>/dev/null
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
  "$binary" daemon stop --socket "$socket_path" --output json >"$temp_dir/stop.json"
  wait "$daemon_pid"
  daemon_pid=""
}

extract_id() {
  prefix=$1
  path=$2
  sed -n "s/.*\"id\":\"\(${prefix}_[0-9a-f]*\)\".*/\1/p" "$path" | sed -n '1p'
}

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key coordination-workspace --output json >"$temp_dir/workspace.json"
"$binary" project add demo --workspace personal --repo "$fixture_root/world-engine" --mode exclusive --socket "$socket_path" --idempotency-key coordination-project --output json >"$temp_dir/project.json"

"$binary" agent create implementer --workspace personal --role implementer --provider fake --runtime fake --max-concurrency 2 --socket "$socket_path" --idempotency-key coordination-agent --output json >"$temp_dir/agent.json"
grep -Fq '"provider":"fake"' "$temp_dir/agent.json"
grep -Fq '"runtime":"fake"' "$temp_dir/agent.json"

"$binary" objective create "Ship greeting" --workspace personal --project demo --budget-tokens 20000 --budget-cents 500 --budget-seconds 3600 --socket "$socket_path" --idempotency-key coordination-objective --output json >"$temp_dir/objective.json"
objective_id=$(extract_id obj "$temp_dir/objective.json")
if [ -z "$objective_id" ]
then
  printf 'objective ID missing\n' >&2
  exit 1
fi

"$binary" task create --workspace personal --project demo --objective "$objective_id" --title "Implement greeting" --priority 200 --budget-tokens 5000 --socket "$socket_path" --idempotency-key coordination-task-main --output json >"$temp_dir/task-main.json"
"$binary" task create --workspace personal --project demo --objective "$objective_id" --title "Document greeting" --priority 100 --socket "$socket_path" --idempotency-key coordination-task-dependent --output json >"$temp_dir/task-dependent.json"
"$binary" task create --workspace personal --project demo --title "Choose wording" --priority 50 --socket "$socket_path" --idempotency-key coordination-task-conflict --output json >"$temp_dir/task-conflict.json"
main_id=$(extract_id task "$temp_dir/task-main.json")
dependent_id=$(extract_id task "$temp_dir/task-dependent.json")
conflict_id=$(extract_id task "$temp_dir/task-conflict.json")
if [ -z "$main_id" ] || [ -z "$dependent_id" ] || [ -z "$conflict_id" ]
then
  printf 'one or more task IDs missing\n' >&2
  exit 1
fi

"$binary" task depend "$dependent_id" --on "$main_id" --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key coordination-dependency --output json >"$temp_dir/dependency.json"
grep -Fq '"ready":false' "$temp_dir/dependency.json"
grep -Fq 'waiting for dependency' "$temp_dir/dependency.json"
if "$binary" task depend "$main_id" --on "$dependent_id" --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key coordination-cycle --output json >"$temp_dir/cycle.out" 2>"$temp_dir/cycle.err"
then
  printf 'circular dependency unexpectedly succeeded\n' >&2
  exit 1
fi
grep -Fq '"code":"dependency_cycle"' "$temp_dir/cycle.err"

"$binary" task list --workspace personal --project demo --ready true --socket "$socket_path" --output json >"$temp_dir/ready.json"
grep -Fq "$main_id" "$temp_dir/ready.json"
grep -Fq "$conflict_id" "$temp_dir/ready.json"
if grep -Fq "$dependent_id" "$temp_dir/ready.json"
then
  printf 'dependency-blocked task appeared in ready-only list\n' >&2
  exit 1
fi
if "$binary" task assign "$dependent_id" implementer --lease-seconds 300 --workspace personal --expected-revision 2 --socket "$socket_path" --idempotency-key coordination-assign-not-ready --output json >"$temp_dir/not-ready.out" 2>"$temp_dir/not-ready.err"
then
  printf 'dependency-blocked assignment unexpectedly succeeded\n' >&2
  exit 1
fi
grep -Fq '"code":"task_not_ready"' "$temp_dir/not-ready.err"

"$binary" task assign "$main_id" implementer --lease-seconds 300 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key coordination-assign --output json >"$temp_dir/assigned.json"
grep -Fq '"status":"assigned"' "$temp_dir/assigned.json"
if "$binary" task assign "$main_id" implementer --lease-seconds 300 --workspace personal --expected-revision 2 --socket "$socket_path" --idempotency-key coordination-double --output json >"$temp_dir/double.out" 2>"$temp_dir/double.err"
then
  printf 'double assignment unexpectedly succeeded\n' >&2
  exit 1
fi
grep -Fq '"code":"assignment_conflict"' "$temp_dir/double.err"

"$binary" task start "$main_id" --workspace personal --expected-revision 2 --socket "$socket_path" --idempotency-key coordination-start --output json >"$temp_dir/started.json"
"$binary" task block "$main_id" --reason "waiting for owner input" --workspace personal --expected-revision 3 --socket "$socket_path" --idempotency-key coordination-block --output json >"$temp_dir/blocked.json"
"$binary" task unblock "$main_id" --workspace personal --expected-revision 4 --socket "$socket_path" --idempotency-key coordination-unblock --output json >"$temp_dir/unblocked.json"
"$binary" task cancel "$main_id" --workspace personal --expected-revision 5 --socket "$socket_path" --idempotency-key coordination-cancel --output json >"$temp_dir/cancelled.json"
grep -Fq '"status":"active"' "$temp_dir/started.json"
grep -Fq '"status":"blocked"' "$temp_dir/blocked.json"
grep -Fq '"status":"assigned"' "$temp_dir/unblocked.json"
grep -Fq '"status":"cancelled"' "$temp_dir/cancelled.json"

"$binary" task update "$conflict_id" --title "Wording A" --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key coordination-writer-a --output json >"$temp_dir/update-a.json"
if "$binary" task update "$conflict_id" --title "Wording B" --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key coordination-writer-b --output json >"$temp_dir/update-b.out" 2>"$temp_dir/update-b.err"
then
  printf 'stale revision update unexpectedly succeeded\n' >&2
  exit 1
fi
grep -Fq '"code":"revision_conflict"' "$temp_dir/update-b.err"

"$binary" status --workspace personal --socket "$socket_path" --output json >"$temp_dir/status-before.json"
grep -Fq '"agents_registered":1' "$temp_dir/status-before.json"
grep -Fq '"tasks_registered":3' "$temp_dir/status-before.json"
grep -Fq '"tasks_cancelled":1' "$temp_dir/status-before.json"
"$binary" agent list --workspace personal --socket "$socket_path" --output json >"$temp_dir/agents-before.json"
"$binary" objective list --workspace personal --project demo --socket "$socket_path" --output json >"$temp_dir/objectives-before.json"
"$binary" task list --workspace personal --project demo --socket "$socket_path" --output json >"$temp_dir/tasks-before.json"
"$binary" events list --after 0 --limit 100 --socket "$socket_path" --output json >"$temp_dir/events-before.json"

stop_daemon
start_daemon
"$binary" status --workspace personal --socket "$socket_path" --output json >"$temp_dir/status-after.json"
"$binary" agent list --workspace personal --socket "$socket_path" --output json >"$temp_dir/agents-after.json"
"$binary" objective list --workspace personal --project demo --socket "$socket_path" --output json >"$temp_dir/objectives-after.json"
"$binary" task list --workspace personal --project demo --socket "$socket_path" --output json >"$temp_dir/tasks-after.json"
"$binary" events list --after 0 --limit 100 --socket "$socket_path" --output json >"$temp_dir/events-after.json"
cmp "$temp_dir/status-before.json" "$temp_dir/status-after.json"
cmp "$temp_dir/agents-before.json" "$temp_dir/agents-after.json"
cmp "$temp_dir/objectives-before.json" "$temp_dir/objectives-after.json"
cmp "$temp_dir/tasks-before.json" "$temp_dir/tasks-after.json"
cmp "$temp_dir/events-before.json" "$temp_dir/events-after.json"
stop_daemon

printf 'Durable coordination acceptance: PASS\n'
