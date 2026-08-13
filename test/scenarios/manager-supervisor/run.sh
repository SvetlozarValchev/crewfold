#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-manager-supervisor.XXXXXX")
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
  if [ ! -d "$data_dir/runtime" ]
  then
    return
  fi
  find "$data_dir/runtime" -type f -name state.json | while IFS= read -r state_path
  do
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
    printf 'manager/supervisor acceptance failed; collected diagnostics follow\n' >&2
    if [ -f "$daemon_log" ]
    then
      printf '%s\n' "$daemon_log" >&2
      sed -n '1,400p' "$daemon_log" >&2
    fi
    find "$scenario_root" -maxdepth 1 -type f \( -name '*.json' -o -name '*.err' -o -name '*.txt' \) -print | sort | while IFS= read -r diagnostic
    do
      printf '%s\n' "$diagnostic" >&2
      sed -n '1,240p' "$diagnostic" >&2
    done
    if [ -d "$data_dir/runtime" ]
    then
      find "$data_dir/runtime" -type f \( -name 'stderr.log' -o -name 'stdout.log' \) -print | sort | while IFS= read -r diagnostic
      do
        printf '%s\n' "$diagnostic" >&2
        sed -n '1,240p' "$diagnostic" >&2
      done
    fi
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

start_daemon() {
  "$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" 2>>"$daemon_log" &
  daemon_pid=$!
  attempts=0
  while ! "$binary" status --socket "$socket_path" --output json >"$scenario_root/daemon-status.json" 2>/dev/null
  do
    if ! kill -0 "$daemon_pid" 2>/dev/null
    then
      wait "$daemon_pid" || true
      sed -n '1,400p' "$daemon_log" >&2
      exit 1
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 300 ]
    then
      printf 'timed out waiting for daemon readiness\n' >&2
      exit 1
    fi
    sleep 0.01
  done
}

stop_daemon() {
  "$binary" daemon stop --socket "$socket_path" --output json >"$scenario_root/daemon-stop.json"
  wait "$daemon_pid"
  daemon_pid=""
}

extract_id() {
  prefix=$1
  path=$2
  sed -n "s/.*\"id\":\"\(${prefix}_[0-9a-f]*\)\".*/\1/p" "$path" | sed -n '1p'
}

extract_scheduled_run() {
  path=$1
  sed -n 's/.*"scheduled_run_ids":\["\(run_[0-9a-f]*\)".*/\1/p' "$path" | sed -n '1p'
}

extract_context_packet() {
  path=$1
  sed -n 's/.*"context_packet_id":"\(ctx_[0-9a-f]*\)".*/\1/p' "$path" | sed -n '1p'
}

wait_run_terminal() {
  run_id=$1
  output=$2
  "$binary" run watch "$run_id" --workspace personal --wait-seconds 15 --socket "$socket_path" --output json >"$output"
  grep -Eq '"status":"(completed|review|start_failed|failed|stopped|lost)"' "$output"
}

wait_for_grant_proposal() {
  grant_id=$1
  output=$2
  attempts=0
  while :
  do
    "$binary" proposal list --workspace personal --grant "$grant_id" --socket "$socket_path" --output json >"$output"
    if grep -Fq '"id":"mprop_' "$output"
    then
      return
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 500 ]
    then
      printf 'manager proposal for grant %s never appeared\n' "$grant_id" >&2
      exit 1
    fi
    sleep 0.01
  done
}

assert_one_run_for_task() {
  task_id=$1
  output=$2
  "$binary" run list --workspace personal --task "$task_id" --socket "$socket_path" --output json >"$output"
  run_count=$(grep -o '"id":"run_[0-9a-f]*"' "$output" | wc -l | tr -d ' ')
  if [ "$run_count" -ne 1 ]
  then
    printf 'task %s has %s runs; wanted exactly one\n' "$task_id" "$run_count" >&2
    exit 1
  fi
}

wait_for_one_run_for_task() {
  task_id=$1
  output=$2
  attempts=0
  while :
  do
    "$binary" run list --workspace personal --task "$task_id" --socket "$socket_path" --output json >"$output"
    run_id=$(extract_id run "$output")
    if [ -n "$run_id" ]
    then
      assert_one_run_for_task "$task_id" "$output"
      printf '%s\n' "$run_id"
      return
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 500 ]
    then
      printf 'background supervisor never scheduled task %s\n' "$task_id" >&2
      exit 1
    fi
    sleep 0.01
  done
}

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >"$scenario_root/fixture-paths.txt"

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key manager-supervisor-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add demo --workspace personal --repo "$fixture_root/world-engine" --mode shared --socket "$socket_path" --idempotency-key manager-supervisor-project --output json >"$scenario_root/project.json"
project_id=$(extract_id prj "$scenario_root/project.json")

# These two definitions intentionally share a non-enumerated descriptive role.
# Only constellation-granted receives an exact manager grant.
"$binary" agent create constellation-granted --workspace personal --role 'constellation cartographer' --provider fixture-mcp --runtime direct --max-concurrency 2 --socket "$socket_path" --idempotency-key manager-supervisor-granted-agent --output json >"$scenario_root/granted-agent.json"
"$binary" agent create constellation-ungranted --workspace personal --role 'constellation cartographer' --provider fixture-mcp --runtime direct --max-concurrency 2 --socket "$socket_path" --idempotency-key manager-supervisor-ungranted-agent --output json >"$scenario_root/ungranted-agent.json"
"$binary" agent create exact-builder --workspace personal --role 'delta artisan' --provider fixture-mcp --runtime direct --max-concurrency 1 --socket "$socket_path" --idempotency-key manager-supervisor-builder-agent --output json >"$scenario_root/builder-agent.json"
"$binary" agent create exact-verifier --workspace personal --role 'independent lens' --provider fixture-mcp --runtime direct --max-concurrency 1 --socket "$socket_path" --idempotency-key manager-supervisor-verifier-agent --output json >"$scenario_root/verifier-agent.json"
grep -Fq '"role":"constellation cartographer"' "$scenario_root/granted-agent.json"
grep -Fq '"role":"constellation cartographer"' "$scenario_root/ungranted-agent.json"

"$binary" objective create 'Bounded manager plan' --workspace personal --project demo --budget-tokens 50000 --budget-cents 2000 --budget-seconds 10000 --socket "$socket_path" --idempotency-key manager-supervisor-objective --output json >"$scenario_root/objective.json"
objective_id=$(extract_id obj "$scenario_root/objective.json")

"$binary" task create --workspace personal --project demo --objective "$objective_id" --title 'Plan exact work' --priority 100 --budget-tokens 1000 --budget-cents 50 --budget-seconds 300 --socket "$socket_path" --idempotency-key manager-supervisor-planning-task --output json >"$scenario_root/planning-task.json"
planning_task=$(extract_id task "$scenario_root/planning-task.json")
"$binary" task assign "$planning_task" constellation-granted --lease-seconds 900 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key manager-supervisor-planning-assignment --output json >"$scenario_root/planning-assignment.json"

"$binary" task create --workspace personal --project demo --objective "$objective_id" --title 'Ungrantable same-role probe' --priority 10 --budget-tokens 1000 --budget-cents 50 --budget-seconds 300 --socket "$socket_path" --idempotency-key manager-supervisor-ungranted-task --output json >"$scenario_root/ungranted-task.json"
ungranted_task=$(extract_id task "$scenario_root/ungranted-task.json")
"$binary" task assign "$ungranted_task" constellation-ungranted --lease-seconds 900 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key manager-supervisor-ungranted-assignment --output json >"$scenario_root/ungranted-assignment.json"

"$binary" launch-profile create --workspace personal --project demo --agent exact-builder --expected-agent-revision 1 --purpose 'exact implementation eligibility' --runtime direct --provider fixture-mcp --scenario "$repo_root/test/fixtures/manager-supervisor/worker-success.json" --assignment-lease-seconds 900 --capability-ttl-seconds 900 --socket "$socket_path" --idempotency-key manager-supervisor-builder-profile --output json >"$scenario_root/builder-profile.json"
builder_profile=$(extract_id lprof "$scenario_root/builder-profile.json")
"$binary" launch-profile create --workspace personal --project demo --agent exact-verifier --expected-agent-revision 1 --purpose 'exact review eligibility' --runtime direct --provider fixture-mcp --scenario "$repo_root/test/fixtures/manager-supervisor/worker-success.json" --assignment-lease-seconds 900 --capability-ttl-seconds 900 --socket "$socket_path" --idempotency-key manager-supervisor-review-profile --output json >"$scenario_root/review-profile.json"
review_profile=$(extract_id lprof "$scenario_root/review-profile.json")

sed -e "s/@IMPLEMENT_PROFILE@/$builder_profile/g" -e "s/@REVIEW_PROFILE@/$review_profile/g" \
  "$repo_root/test/fixtures/manager-supervisor/manager-plan.json.in" >"$scenario_root/manager-plan.json"

"$binary" manager grant create --workspace personal --project demo --objective "$objective_id" --task "$planning_task" --agent constellation-granted --expected-task-revision 2 --expected-agent-revision 1 --proposal-kinds task_decomposition --launch-profiles "$builder_profile,$review_profile" --claim-kinds component --max-open-proposals 4 --max-actions 16 --max-tasks 8 --max-dependencies 8 --max-claim-requirements 4 --token-limit 12000 --cost-cents 400 --time-seconds 3000 --socket "$socket_path" --idempotency-key manager-supervisor-grant --output json >"$scenario_root/grant.json"
grant_id=$(extract_id mgrgrant "$scenario_root/grant.json")
"$binary" launch-profile create --workspace personal --project demo --agent constellation-granted --expected-agent-revision 1 --purpose 'planning workflow metadata only' --runtime direct --provider fixture-mcp --scenario "$scenario_root/manager-plan.json" --assignment-lease-seconds 900 --capability-ttl-seconds 900 --manager-grant "$grant_id" --socket "$socket_path" --idempotency-key manager-supervisor-planning-profile --output json >"$scenario_root/planning-profile.json"
planning_profile=$(extract_id lprof "$scenario_root/planning-profile.json")

# An identical Role label under packet v4 receives none of the four manager tools.
"$binary" run start "$ungranted_task" --workspace personal --runtime direct --provider fixture-mcp --scenario "$repo_root/test/fixtures/manager-supervisor/ungranted-role-probe.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key manager-supervisor-ungranted-run --output json >"$scenario_root/ungranted-run.json"
ungranted_run=$(extract_id run "$scenario_root/ungranted-run.json")
ungranted_context=$(extract_context_packet "$scenario_root/ungranted-run.json")
"$binary" context show "$ungranted_context" --workspace personal --socket "$socket_path" --output json >"$scenario_root/ungranted-context.json"
grep -Fq 'urn:crewfold:schema:domain:context-packet:v4' "$scenario_root/ungranted-context.json"
if grep -Eq 'crewfold_propose_(tasks|assignment|review|escalation)' "$scenario_root/ungranted-context.json" || grep -Fq '"management_grant"' "$scenario_root/ungranted-context.json"
then
  printf 'ungranted packet-v4 run received manager authority\n' >&2
  exit 1
fi
wait_run_terminal "$ungranted_run" "$scenario_root/ungranted-final.json"
grep -Fq '"status":"completed"' "$scenario_root/ungranted-final.json"

"$binary" manager propose-tasks --workspace personal --objective "$objective_id" --planning-task "$planning_task" --grant "$grant_id" --profile "$planning_profile" --expected-task-revision 2 --expected-grant-revision 1 --expected-profile-revision 1 --socket "$socket_path" --idempotency-key manager-supervisor-invoke --output json >"$scenario_root/manager-run.json"
manager_run=$(extract_id run "$scenario_root/manager-run.json")
manager_context=$(extract_context_packet "$scenario_root/manager-run.json")
"$binary" context show "$manager_context" --workspace personal --socket "$socket_path" --output json >"$scenario_root/manager-context.json"
grep -Fq 'urn:crewfold:schema:domain:context-packet:v5' "$scenario_root/manager-context.json"
grep -Fq 'crewfold_propose_tasks' "$scenario_root/manager-context.json"
grep -Fq "$grant_id" "$scenario_root/manager-context.json"
wait_run_terminal "$manager_run" "$scenario_root/manager-final.json"
grep -Fq '"status":"completed"' "$scenario_root/manager-final.json"

"$binary" proposal list --workspace personal --grant "$grant_id" --status pending --socket "$socket_path" --output json >"$scenario_root/proposals-pending.json"
proposal_id=$(extract_id mprop "$scenario_root/proposals-pending.json")
"$binary" proposal inspect "$proposal_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/proposal-before.json"
grep -Fq '"status":"pending"' "$scenario_root/proposal-before.json"
grep -Fq '"type":"create_task"' "$scenario_root/proposal-before.json"

# Submission is inert: the proposed titles do not exist until owner acceptance.
"$binary" task list --workspace personal --project demo --socket "$socket_path" >"$scenario_root/tasks-before.txt"
if grep -Fq 'Implement A' "$scenario_root/tasks-before.txt" || grep -Fq 'Implement B' "$scenario_root/tasks-before.txt" || grep -Fq 'Independently review A and B' "$scenario_root/tasks-before.txt"
then
  printf 'pending proposal mutated the task graph\n' >&2
  exit 1
fi

"$binary" proposal accept "$proposal_id" --workspace personal --expected-revision 1 --decision-note 'Accept the exact bounded A to B to review graph.' --socket "$socket_path" --idempotency-key manager-supervisor-accept --output json >"$scenario_root/proposal-accepted.json"
"$binary" proposal accept "$proposal_id" --workspace personal --expected-revision 1 --decision-note 'Accept the exact bounded A to B to review graph.' --socket "$socket_path" --idempotency-key manager-supervisor-accept --output json >"$scenario_root/proposal-accepted-replay.json"
cmp "$scenario_root/proposal-accepted.json" "$scenario_root/proposal-accepted-replay.json"
grep -Fq '"status":"accepted"' "$scenario_root/proposal-accepted.json"

"$binary" task list --workspace personal --project demo --socket "$socket_path" >"$scenario_root/tasks-after.txt"
task_a=$(awk -F '\t' '$3 == "Implement A" {print $1}' "$scenario_root/tasks-after.txt")
task_b=$(awk -F '\t' '$3 == "Implement B" {print $1}' "$scenario_root/tasks-after.txt")
task_review=$(awk -F '\t' '$3 == "Independently review A and B" {print $1}' "$scenario_root/tasks-after.txt")
if [ -z "$task_a" ] || [ -z "$task_b" ] || [ -z "$task_review" ]
then
  printf 'accepted proposal did not create the exact three-task graph\n' >&2
  exit 1
fi

# Accepted intents are durable before scheduling. Restart the daemon while all
# three are pending, then use one explicit scan for replay coverage and rely on
# the background loop for the dependent and review progression.
stop_daemon
start_daemon

project_limits=$(printf '{"%s":1}' "$project_id")
"$binary" supervisor policy update --workspace personal --enabled true --auto-schedule true --auto-retry-limit 1 --retry-cooldown-seconds 1 --max-active-runs 2 --max-starting-runs 1 --default-project-concurrency 1 --default-provider-concurrency 1 --project-concurrency-json "$project_limits" --provider-concurrency-json '{"fixture-mcp":1}' --expected-revision 1 --socket "$socket_path" --idempotency-key manager-supervisor-policy --output json >"$scenario_root/policy.json"

"$binary" supervisor run --workspace personal --limit 100 --socket "$socket_path" --idempotency-key manager-supervisor-schedule-a --output json >"$scenario_root/supervisor-a.json"
"$binary" supervisor run --workspace personal --limit 100 --socket "$socket_path" --idempotency-key manager-supervisor-schedule-a --output json >"$scenario_root/supervisor-a-replay.json"
cmp "$scenario_root/supervisor-a.json" "$scenario_root/supervisor-a-replay.json"
run_a=$(extract_scheduled_run "$scenario_root/supervisor-a.json")
assert_one_run_for_task "$task_a" "$scenario_root/runs-a.json"
wait_run_terminal "$run_a" "$scenario_root/run-a-final.json"
grep -Fq '"status":"completed"' "$scenario_root/run-a-final.json"

run_b=$(wait_for_one_run_for_task "$task_b" "$scenario_root/runs-b.json")
wait_run_terminal "$run_b" "$scenario_root/run-b-final.json"
grep -Fq '"status":"completed"' "$scenario_root/run-b-final.json"

run_review=$(wait_for_one_run_for_task "$task_review" "$scenario_root/runs-review.json")
wait_run_terminal "$run_review" "$scenario_root/run-review-final.json"
grep -Fq '"status":"completed"' "$scenario_root/run-review-final.json"
"$binary" supervisor list --workspace personal --condition dependency_ready --socket "$socket_path" --output json >"$scenario_root/supervisor-actions.json"
grep -Fq '"status":"applied"' "$scenario_root/supervisor-actions.json"
"$binary" supervisor explain --workspace personal --task "$task_b" --socket "$socket_path" --output json >"$scenario_root/supervisor-explain.json"
grep -Fq '"type":"supervisor_explanation"' "$scenario_root/supervisor-explain.json"

# A live packet-v5 snapshot does not survive owner revocation for later calls.
"$binary" task create --workspace personal --project demo --objective "$objective_id" --title 'Revocation assignment target' --priority 20 --budget-tokens 1000 --budget-cents 50 --budget-seconds 300 --socket "$socket_path" --idempotency-key manager-supervisor-revocation-target --output json >"$scenario_root/revocation-target.json"
revocation_target=$(extract_id task "$scenario_root/revocation-target.json")
"$binary" task create --workspace personal --project demo --objective "$objective_id" --title 'Revocation planning task' --priority 20 --budget-tokens 1000 --budget-cents 50 --budget-seconds 300 --socket "$socket_path" --idempotency-key manager-supervisor-revocation-planning --output json >"$scenario_root/revocation-planning.json"
revocation_planning=$(extract_id task "$scenario_root/revocation-planning.json")
"$binary" task assign "$revocation_planning" constellation-granted --lease-seconds 900 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key manager-supervisor-revocation-planning-assignment --output json >"$scenario_root/revocation-planning-assignment.json"
"$binary" manager grant create --workspace personal --project demo --objective "$objective_id" --task "$revocation_planning" --agent constellation-granted --expected-task-revision 2 --expected-agent-revision 1 --proposal-kinds assignment --launch-profiles "$builder_profile" --max-open-proposals 2 --max-actions 2 --max-tasks 1 --max-dependencies 1 --max-claim-requirements 1 --token-limit 1000 --cost-cents 100 --time-seconds 600 --socket "$socket_path" --idempotency-key manager-supervisor-revocation-grant --output json >"$scenario_root/revocation-grant.json"
revocation_grant=$(extract_id mgrgrant "$scenario_root/revocation-grant.json")
sed -e "s/@TARGET_TASK@/$revocation_target/g" -e 's/@TARGET_TASK_REVISION@/1/g' -e "s/@IMPLEMENT_PROFILE@/$builder_profile/g" \
  "$repo_root/test/fixtures/manager-supervisor/revocation-probe.json.in" >"$scenario_root/revocation-probe.json"
"$binary" launch-profile create --workspace personal --project demo --agent constellation-granted --expected-agent-revision 1 --runtime direct --provider fixture-mcp --scenario "$scenario_root/revocation-probe.json" --assignment-lease-seconds 900 --capability-ttl-seconds 900 --manager-grant "$revocation_grant" --socket "$socket_path" --idempotency-key manager-supervisor-revocation-profile --output json >"$scenario_root/revocation-profile.json"
revocation_profile=$(extract_id lprof "$scenario_root/revocation-profile.json")
"$binary" manager propose-tasks --workspace personal --objective "$objective_id" --planning-task "$revocation_planning" --grant "$revocation_grant" --profile "$revocation_profile" --expected-task-revision 2 --expected-grant-revision 1 --expected-profile-revision 1 --socket "$socket_path" --idempotency-key manager-supervisor-revocation-invoke --output json >"$scenario_root/revocation-run.json"
revocation_run=$(extract_id run "$scenario_root/revocation-run.json")
wait_for_grant_proposal "$revocation_grant" "$scenario_root/revocation-proposals-before.json"
"$binary" manager grant revoke "$revocation_grant" --workspace personal --expected-revision 1 --reason 'Exercise live fail-closed revocation.' --socket "$socket_path" --idempotency-key manager-supervisor-revoke --output json >"$scenario_root/revoked.json"
wait_run_terminal "$revocation_run" "$scenario_root/revocation-final.json"
grep -Fq '"status":"completed"' "$scenario_root/revocation-final.json"
"$binary" proposal list --workspace personal --grant "$revocation_grant" --socket "$socket_path" --output json >"$scenario_root/revocation-proposals-after.json"
revocation_proposal_count=$(grep -o '"id":"mprop_[0-9a-f]*"' "$scenario_root/revocation-proposals-after.json" | wc -l | tr -d ' ')
if [ "$revocation_proposal_count" -ne 1 ]
then
  printf 'revoked manager produced %s proposals; wanted only the pre-revocation proposal\n' "$revocation_proposal_count" >&2
  exit 1
fi

stop_daemon
printf 'Manager proposal and deterministic supervisor public smoke: PASS\n'
