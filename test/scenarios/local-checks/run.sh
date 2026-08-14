#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-local-checks.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
fixture_root="$scenario_root/git-fixture"
checkout_path="$fixture_root/world-engine"
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
  if [ ! -d "$data_dir" ]
  then
    return
  fi
  find "$data_dir" -type f -name state.json | while IFS= read -r state_path
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
    printf 'owner-granted local-check acceptance failed\n' >&2
    if [ -f "$scenario_root/failed-check.json" ]
    then
      printf 'failed-check.json:\n' >&2
      sed -n '1,20p' "$scenario_root/failed-check.json" >&2
    fi
    if [ -f "$daemon_log" ]
    then
      tail -n 200 "$daemon_log" >&2
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
      sed -n '1,500p' "$daemon_log" >&2
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

wait_run_blocked() {
  run_id=$1
  output=$2
  attempts=0
  while :
  do
    "$binary" run show "$run_id" --workspace personal --socket "$socket_path" --output json >"$output"
    if grep -Fq '"status":"blocked"' "$output"
    then
      return
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 400 ]
    then
      printf 'checkout-anchor run %s did not block durably\n' "$run_id" >&2
      exit 1
    fi
    sleep 0.025
  done
}

wait_check_terminal() {
  check_run=$1
  output=$2
  attempts=0
  while :
  do
    "$binary" check inspect "$check_run" --workspace personal --socket "$socket_path" --output json >"$output"
    if grep -Fq '"status":"finished"' "$output" && grep -Fq '"result":{' "$output"
    then
      return
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 600 ]
    then
      printf 'check run %s did not reach a durable terminal result\n' "$check_run" >&2
      exit 1
    fi
    sleep 0.025
  done
}

wait_first_check_for_task() {
  task_id=$1
  output=$2
  attempts=0
  while :
  do
    "$binary" check list --workspace personal --task "$task_id" --limit 100 --socket "$socket_path" --output json >"$output"
    check_run=$(extract_id checkrun "$output")
    if [ -n "$check_run" ]
    then
      printf '%s\n' "$check_run"
      return
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 400 ]
    then
      printf 'granted watcher did not request a check for task %s\n' "$task_id" >&2
      exit 1
    fi
    sleep 0.025
  done
}

wait_marker() {
  marker=$1
  attempts=0
  while [ ! -s "$marker" ]
  do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 400 ]
    then
      printf 'recovery child never recorded its launch marker\n' >&2
      exit 1
    fi
    sleep 0.025
  done
}

count_tasks() {
  path=$1
  grep -o '"id":"task_[0-9a-f]*"' "$path" | sort -u | wc -l | tr -d ' '
}

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >"$scenario_root/fixture-paths.txt"
printf 'FAIL\n' >"$checkout_path/check-state.txt"
git -C "$checkout_path" add check-state.txt
git -C "$checkout_path" commit --quiet -m 'add failing check state'

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key local-check-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add demo --workspace personal --repo "$checkout_path" --mode shared --socket "$socket_path" --idempotency-key local-check-project --output json >"$scenario_root/project.json"
project_id=$(extract_id prj "$scenario_root/project.json")
checkout_id=$(extract_id co "$scenario_root/project.json")

# The role string is deliberately arbitrary and identical. It is display text;
# the exact grant, assignment, route, and launch profile supply every authority.
for agent in change-agent watcher-granted watcher-ungranted repair-agent
do
  "$binary" agent create "$agent" --workspace personal --role 'aurora field notebook' --provider fixture-mcp --runtime direct --max-concurrency 2 --socket "$socket_path" --idempotency-key "local-check-agent-$agent" --output json >"$scenario_root/$agent.json"
  grep -Fq '"role":"aurora field notebook"' "$scenario_root/$agent.json"
done
watcher_agent_id=$(extract_id agent "$scenario_root/watcher-granted.json")

"$binary" objective create 'Fresh local mechanical evidence' --workspace personal --project demo --budget-tokens 20000 --budget-cents 1000 --budget-seconds 10000 --socket "$socket_path" --idempotency-key local-check-objective --output json >"$scenario_root/objective.json"
objective_id=$(extract_id obj "$scenario_root/objective.json")

create_assigned_task() {
  name=$1
  title=$2
  agent=$3
  "$binary" task create --workspace personal --project demo --objective "$objective_id" --title "$title" --priority 50 --budget-tokens 1000 --budget-cents 100 --budget-seconds 600 --socket "$socket_path" --idempotency-key "local-check-task-$name" --output json >"$scenario_root/task-$name.json"
  task_id=$(extract_id task "$scenario_root/task-$name.json")
  "$binary" task assign "$task_id" "$agent" --lease-seconds 1800 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key "local-check-assign-$name" --output json >"$scenario_root/assign-$name.json"
  printf '%s\n' "$task_id"
}

target_task=$(create_assigned_task target 'Change guarded by one exact local check' change-agent)
ungranted_task=$(create_assigned_task ungranted 'Same-role ungranted check probe' watcher-ungranted)
request_task=$(create_assigned_task request 'Request exact allowlisted check' watcher-granted)
repair_task=$(create_assigned_task repair 'Inspect exact failed check evidence' watcher-granted)
stale_repair_task=$(create_assigned_task stale-repair 'Propose repair that a later pass stales' watcher-granted)
recovery_task=$(create_assigned_task recovery 'Recover one durable local check child' change-agent)

"$binary" check definition create state-check --workspace personal --project demo --executable "$repo_root/test/fixtures/local-checks/check.sh" --arg state --working-directory . --timeout 10s --output-byte-limit 1024 --socket "$socket_path" --idempotency-key local-check-definition-state --output json >"$scenario_root/definition-state.json"
state_definition=$(extract_id checkdef "$scenario_root/definition-state.json")
"$binary" check requirement create --workspace personal --task "$target_task" --criterion exact-state --statement 'The allowlisted repository state check passes at current HEAD.' --definition "$state_definition" --definition-content-revision 1 --expected-task-revision 2 --socket "$socket_path" --idempotency-key local-check-requirement-state --output json >"$scenario_root/requirement-state.json"
state_requirement=$(extract_id checkreq "$scenario_root/requirement-state.json")

recovery_marker="$scenario_root/recovery-launches.txt"
"$binary" check definition create recovery-check --workspace personal --project demo --executable "$repo_root/test/fixtures/local-checks/check.sh" --arg recovery --arg "$recovery_marker" --working-directory . --timeout 10s --output-byte-limit 1024 --socket "$socket_path" --idempotency-key local-check-definition-recovery --output json >"$scenario_root/definition-recovery.json"
recovery_definition=$(extract_id checkdef "$scenario_root/definition-recovery.json")
"$binary" check requirement create --workspace personal --task "$recovery_task" --criterion exact-recovery --statement 'The exact durable recovery check passes once.' --definition "$recovery_definition" --definition-content-revision 1 --expected-task-revision 2 --socket "$socket_path" --idempotency-key local-check-requirement-recovery --output json >"$scenario_root/requirement-recovery.json"

"$binary" launch-profile create --workspace personal --project demo --agent repair-agent --expected-agent-revision 1 --purpose 'arbitrary repair recipe metadata' --runtime direct --provider fixture-mcp --scenario "$repo_root/test/fixtures/manager-supervisor/worker-success.json" --assignment-lease-seconds 900 --capability-ttl-seconds 900 --socket "$socket_path" --idempotency-key local-check-repair-profile --output json >"$scenario_root/repair-profile.json"
repair_profile=$(extract_id lprof "$scenario_root/repair-profile.json")
"$binary" check policy configure --workspace personal --project demo --repair-proposals enabled --repair-profile "$repair_profile" --repair-profile-revision 1 --max-open-repairs 4 --expected-revision 1 --socket "$socket_path" --idempotency-key local-check-policy --output json >"$scenario_root/check-policy.json"

"$binary" check grant create --workspace personal --project demo --agent watcher-granted --expected-agent-revision 1 --definition "$state_definition@1" --operation run --operation inspect --operation propose_repair --max-pending 4 --max-in-flight 2 --socket "$socket_path" --idempotency-key local-check-grant --output json >"$scenario_root/check-grant.json"
grant_id=$(extract_id checkgrant "$scenario_root/check-grant.json")

for trigger in nonpass pass stale
do
  "$binary" check route create --workspace personal --project demo --definition "$state_definition" --definition-content-revision 1 --trigger "$trigger" --duty evidence_review --agent watcher-granted --expected-agent-revision 1 --socket "$socket_path" --idempotency-key "local-check-route-$trigger" --output json >"$scenario_root/route-$trigger.json"
done

# The checked task has its own durable run/checkout binding. A blocked anchor
# retains its exact assignment while leaving mechanical verification to checks.
"$binary" run start "$target_task" --workspace personal --checkout "$checkout_id" --runtime direct --provider fixture-mcp --scenario "$repo_root/test/fixtures/local-checks/anchor.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key local-check-target-anchor --output json >"$scenario_root/target-anchor-run.json"
target_anchor_run=$(extract_id run "$scenario_root/target-anchor-run.json")
wait_run_blocked "$target_anchor_run" "$scenario_root/target-anchor-final.json"

# An identical role under the current packet gets no check grant or tools and
# creates no check run.
"$binary" run start "$ungranted_task" --workspace personal --checkout "$checkout_id" --runtime direct --provider fixture-mcp --scenario "$repo_root/test/fixtures/local-checks/ungranted.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key local-check-ungranted-run --output json >"$scenario_root/ungranted-run.json"
ungranted_run=$(extract_id run "$scenario_root/ungranted-run.json")
ungranted_context=$(extract_context_packet "$scenario_root/ungranted-run.json")
"$binary" context show "$ungranted_context" --workspace personal --socket "$socket_path" --output json >"$scenario_root/ungranted-context.json"
grep -Fq 'urn:crewfold:schema:domain:context-packet:v1' "$scenario_root/ungranted-context.json"
if grep -Eq 'crewfold_(run_check|list_check_results|inspect_check_result|propose_check_repair)' "$scenario_root/ungranted-context.json" || grep -Fq '"check_watch_grant"' "$scenario_root/ungranted-context.json"
then
  printf 'same-role ungranted packet received check authority\n' >&2
  exit 1
fi
wait_run_terminal "$ungranted_run" "$scenario_root/ungranted-final.json"
grep -Fq '"status":"completed"' "$scenario_root/ungranted-final.json"
"$binary" check list --workspace personal --task "$target_task" --socket "$socket_path" --output json >"$scenario_root/checks-before-grant.json"
if grep -Fq '"id":"checkrun_' "$scenario_root/checks-before-grant.json"
then
  printf 'ungranted same-role probe created a check run\n' >&2
  exit 1
fi

# The exact current-packet grant lets the chosen arbitrary-role agent request only
# the frozen requirement and command.
sed "s/@REQUIREMENT@/$state_requirement/g" "$repo_root/test/fixtures/local-checks/request-check.json.in" >"$scenario_root/request-check.json"
head_before_failure=$(git -C "$checkout_path" rev-parse HEAD)
"$binary" task show "$target_task" --workspace personal --socket "$socket_path" --output json >"$scenario_root/target-before.json"
target_status_before=$(grep -o '"status":"[^"]*"' "$scenario_root/target-before.json" | sed -n '1s/"status":"\([^"]*\)"/\1/p')
target_revision_before=$(grep -o '"revision":[0-9][0-9]*' "$scenario_root/target-before.json" | sed -n '1s/"revision"://p')
test -n "$target_status_before"
test -n "$target_revision_before"
"$binary" run start "$request_task" --workspace personal --checkout "$checkout_id" --runtime direct --provider fixture-mcp --scenario "$scenario_root/request-check.json" --expected-task-revision 2 --check-watch-grant "$grant_id" --expected-check-watch-grant-revision 1 --socket "$socket_path" --idempotency-key local-check-granted-run --output json >"$scenario_root/granted-run.json"
granted_run=$(extract_id run "$scenario_root/granted-run.json")
granted_context=$(extract_context_packet "$scenario_root/granted-run.json")
"$binary" context show "$granted_context" --workspace personal --socket "$socket_path" --output json >"$scenario_root/granted-context.json"
grep -Fq 'urn:crewfold:schema:domain:context-packet:v1' "$scenario_root/granted-context.json"
grep -Fq "$grant_id" "$scenario_root/granted-context.json"
for tool in crewfold_run_check crewfold_list_check_results crewfold_inspect_check_result crewfold_propose_check_repair
do
  grep -Fq '"'"$tool"'"' "$scenario_root/granted-context.json"
done
if grep -Fq '"crewfold_accept_check_repair"' "$scenario_root/granted-context.json"
then
  printf 'agent packet exposed the owner-reserved repair acceptance tool\n' >&2
  exit 1
fi
wait_run_terminal "$granted_run" "$scenario_root/granted-final.json"
grep -Fq '"status":"completed"' "$scenario_root/granted-final.json"
failed_check=$(wait_first_check_for_task "$target_task" "$scenario_root/checks-after-grant.json")
wait_check_terminal "$failed_check" "$scenario_root/failed-check.json"
grep -Fq '"outcome":"failed"' "$scenario_root/failed-check.json"
grep -Fq '"status":"fresh"' "$scenario_root/failed-check.json"
grep -Fq '"requirement_state":"failed"' "$scenario_root/failed-check.json"
grep -Fq '"class":"mechanical_check"' "$scenario_root/failed-check.json"
failed_result=$(extract_id checkresult "$scenario_root/failed-check.json")
"$binary" check logs "$failed_check" --workspace personal --socket "$socket_path" --output json >"$scenario_root/failed-logs.json"
grep -Fq '[REDACTED]' "$scenario_root/failed-logs.json"
if grep -Fq 'fixture-local-check-secret' "$scenario_root/failed-logs.json"
then
  printf 'check artifact leaked secret-shaped fixture output\n' >&2
  exit 1
fi
test "$(git -C "$checkout_path" rev-parse HEAD)" = "$head_before_failure"
"$binary" task show "$target_task" --workspace personal --socket "$socket_path" --output json >"$scenario_root/target-after-failure.json"
grep -Fq '"status":"'"$target_status_before"'"' "$scenario_root/target-after-failure.json"
grep -Fq '"revision":'"$target_revision_before" "$scenario_root/target-after-failure.json"

# Failure routing uses the exact current assignment and exact duty route. The
# ungranted same-role agent receives neither message.
"$binary" inbox --workspace personal --agent change-agent --limit 50 --socket "$socket_path" --output json >"$scenario_root/change-inbox.json"
"$binary" inbox --workspace personal --agent watcher-granted --limit 50 --socket "$socket_path" --output json >"$scenario_root/granted-inbox.json"
"$binary" inbox --workspace personal --agent watcher-ungranted --limit 50 --socket "$socket_path" --output json >"$scenario_root/ungranted-inbox.json"
grep -Fq '"sender_type":"subsystem"' "$scenario_root/change-inbox.json"
grep -Fq '"sender_id":"crewfold-check-worker"' "$scenario_root/change-inbox.json"
grep -Fq '"sender_type":"subsystem"' "$scenario_root/granted-inbox.json"
if grep -Fq '"message":{"id":"msg_' "$scenario_root/ungranted-inbox.json"
then
  printf 'same-role ungranted agent received an exact check route\n' >&2
  exit 1
fi

# The granted watcher may inspect and propose, but the reserved acceptance tool
# is denied. Submission is inert until the local owner decides it.
sed -e "s/@CHECK_RUN@/$failed_check/g" -e "s/@CHECK_RESULT@/$failed_result/g" "$repo_root/test/fixtures/local-checks/inspect-repair.json.in" >"$scenario_root/inspect-repair.json"
"$binary" task list --workspace personal --project demo --socket "$socket_path" --output json >"$scenario_root/tasks-before-proposal.json"
tasks_before_proposal=$(count_tasks "$scenario_root/tasks-before-proposal.json")
"$binary" run start "$repair_task" --workspace personal --checkout "$checkout_id" --runtime direct --provider fixture-mcp --scenario "$scenario_root/inspect-repair.json" --expected-task-revision 2 --check-watch-grant "$grant_id" --expected-check-watch-grant-revision 1 --socket "$socket_path" --idempotency-key local-check-repair-run --output json >"$scenario_root/repair-run.json"
repair_run=$(extract_id run "$scenario_root/repair-run.json")
wait_run_terminal "$repair_run" "$scenario_root/repair-final.json"
grep -Fq '"status":"completed"' "$scenario_root/repair-final.json"
"$binary" check repair list --workspace personal --project demo --status pending --socket "$socket_path" --output json >"$scenario_root/repairs-pending.json"
repair_proposal=$(extract_id checkrepair "$scenario_root/repairs-pending.json")
"$binary" check repair inspect "$repair_proposal" --workspace personal --socket "$socket_path" --output json >"$scenario_root/repair-before.json"
grep -Fq '"status":"pending"' "$scenario_root/repair-before.json"
grep -Fq "$repair_profile" "$scenario_root/repair-before.json"
grep -Fq '"source_run_id":"'"$repair_run"'"' "$scenario_root/repair-before.json"
grep -Fq '"source_agent_id":"'"$watcher_agent_id"'"' "$scenario_root/repair-before.json"
grep -Fq '"source_agent_revision":1' "$scenario_root/repair-before.json"
grep -Fq '"source_grant_id":"'"$grant_id"'"' "$scenario_root/repair-before.json"
grep -Fq '"source_grant_revision":1' "$scenario_root/repair-before.json"
grep -Fq '"created_by":"agent:'"$watcher_agent_id"'"' "$scenario_root/repair-before.json"
if grep -Fq '"decision":{' "$scenario_root/repair-before.json" || grep -Fq '"effect":{' "$scenario_root/repair-before.json"
then
  printf 'watcher repair proposal had a decision or work effect before owner acceptance\n' >&2
  exit 1
fi
"$binary" task list --workspace personal --project demo --socket "$socket_path" --output json >"$scenario_root/tasks-after-proposal.json"
test "$(count_tasks "$scenario_root/tasks-after-proposal.json")" = "$tasks_before_proposal"

"$binary" check repair accept "$repair_proposal" --workspace personal --expected-revision 1 --decision-note 'Accept the frozen exact-profile repair recipe.' --socket "$socket_path" --idempotency-key local-check-repair-accept --output json >"$scenario_root/repair-accepted.json"
"$binary" check repair accept "$repair_proposal" --workspace personal --expected-revision 1 --decision-note 'Accept the frozen exact-profile repair recipe.' --socket "$socket_path" --idempotency-key local-check-repair-accept --output json >"$scenario_root/repair-accepted-replay.json"
cmp "$scenario_root/repair-accepted.json" "$scenario_root/repair-accepted-replay.json"
grep -Fq '"status":"accepted"' "$scenario_root/repair-accepted.json"
grep -Fq '"decision":{"id":"checkdecision_' "$scenario_root/repair-accepted.json"
grep -Fq '"decision":"accepted"' "$scenario_root/repair-accepted.json"
grep -Fq '"proposal_revision":1' "$scenario_root/repair-accepted.json"
grep -Fq '"note":"Accept the frozen exact-profile repair recipe."' "$scenario_root/repair-accepted.json"
grep -Fq '"created_by":"local-owner"' "$scenario_root/repair-accepted.json"
grep -Fq '"effect":{' "$scenario_root/repair-accepted.json"
"$binary" task list --workspace personal --project demo --socket "$socket_path" --output json >"$scenario_root/tasks-after-accept.json"
test "$(count_tasks "$scenario_root/tasks-after-accept.json")" -eq $((tasks_before_proposal + 1))

# A second failure gets a distinct inert proposal. The later fresh pass must
# stale it rather than silently materialize duplicate repair work.
"$binary" check run "$state_definition" --task "$target_task" --workspace personal --checkout "$checkout_id" --expected-requirement-revision 1 --expected-definition-content-revision 1 --socket "$socket_path" --idempotency-key local-check-second-failure --output json >"$scenario_root/second-failure-run.json"
second_failed_check=$(extract_id checkrun "$scenario_root/second-failure-run.json")
wait_check_terminal "$second_failed_check" "$scenario_root/second-failed-check.json"
grep -Fq '"outcome":"failed"' "$scenario_root/second-failed-check.json"
second_failed_result=$(extract_id checkresult "$scenario_root/second-failed-check.json")
sed -e "s/@CHECK_RUN@/$second_failed_check/g" -e "s/@CHECK_RESULT@/$second_failed_result/g" "$repo_root/test/fixtures/local-checks/inspect-repair.json.in" >"$scenario_root/inspect-stale-repair.json"
"$binary" run start "$stale_repair_task" --workspace personal --checkout "$checkout_id" --runtime direct --provider fixture-mcp --scenario "$scenario_root/inspect-stale-repair.json" --expected-task-revision 2 --check-watch-grant "$grant_id" --expected-check-watch-grant-revision 1 --socket "$socket_path" --idempotency-key local-check-stale-repair-run --output json >"$scenario_root/stale-repair-run.json"
stale_repair_run=$(extract_id run "$scenario_root/stale-repair-run.json")
wait_run_terminal "$stale_repair_run" "$scenario_root/stale-repair-final.json"
"$binary" check repair list --workspace personal --project demo --status pending --socket "$socket_path" --output json >"$scenario_root/repairs-before-pass.json"
second_repair=$(extract_id checkrepair "$scenario_root/repairs-before-pass.json")

printf 'PASS\n' >"$checkout_path/check-state.txt"
git -C "$checkout_path" add check-state.txt
git -C "$checkout_path" commit --quiet -m 'make local check pass'
pass_head=$(git -C "$checkout_path" rev-parse HEAD)
"$binary" check run "$state_definition" --task "$target_task" --workspace personal --checkout "$checkout_id" --expected-requirement-revision 1 --expected-definition-content-revision 1 --socket "$socket_path" --idempotency-key local-check-fresh-pass --output json >"$scenario_root/pass-run.json"
pass_check=$(extract_id checkrun "$scenario_root/pass-run.json")
wait_check_terminal "$pass_check" "$scenario_root/pass-check.json"
grep -Fq '"outcome":"passed"' "$scenario_root/pass-check.json"
grep -Fq '"status":"fresh"' "$scenario_root/pass-check.json"
grep -Fq '"requirement_state":"verified"' "$scenario_root/pass-check.json"
grep -Fq "$pass_head" "$scenario_root/pass-check.json"
"$binary" check repair inspect "$second_repair" --workspace personal --socket "$socket_path" --output json >"$scenario_root/stale-repair-after-pass.json"
grep -Fq '"status":"stale"' "$scenario_root/stale-repair-after-pass.json"
"$binary" task show "$target_task" --workspace personal --socket "$socket_path" --output json >"$scenario_root/target-after-pass.json"
grep -Fq '"status":"'"$target_status_before"'"' "$scenario_root/target-after-pass.json"
grep -Fq '"revision":'"$target_revision_before" "$scenario_root/target-after-pass.json"

# A real fresh Git observation, not the cached checkout row, monotonically
# invalidates the former pass. Exact public replay returns one frozen receipt.
printf 'post-pass source change\n' >>"$checkout_path/README.md"
git -C "$checkout_path" add README.md
git -C "$checkout_path" commit --quiet -m 'advance source after passing check'
stale_head=$(git -C "$checkout_path" rev-parse HEAD)
test "$stale_head" != "$pass_head"
"$binary" check watch --workspace personal --project "$project_id" --limit 100 --socket "$socket_path" --idempotency-key local-check-public-watch --output json >"$scenario_root/watch.json"
"$binary" check watch --workspace personal --project "$project_id" --limit 100 --socket "$socket_path" --idempotency-key local-check-public-watch --output json >"$scenario_root/watch-replay.json"
cmp "$scenario_root/watch.json" "$scenario_root/watch-replay.json"
"$binary" check inspect "$pass_check" --workspace personal --socket "$socket_path" --output json >"$scenario_root/pass-after-head-change.json"
grep -Fq '"status":"stale"' "$scenario_root/pass-after-head-change.json"
grep -Fq '"requirement_state":"stale"' "$scenario_root/pass-after-head-change.json"
grep -Fq "$stale_head" "$scenario_root/pass-after-head-change.json"

# Stop after the child has definitely launched but before its terminal result.
# Restart must reconcile the same stable operation and execute it exactly once.
"$binary" check run "$recovery_definition" --task "$recovery_task" --workspace personal --checkout "$checkout_id" --expected-requirement-revision 1 --expected-definition-content-revision 1 --socket "$socket_path" --idempotency-key local-check-recovery --output json >"$scenario_root/recovery-run.json"
recovery_check=$(extract_id checkrun "$scenario_root/recovery-run.json")
wait_marker "$recovery_marker"
stop_daemon
start_daemon
wait_check_terminal "$recovery_check" "$scenario_root/recovery-check.json"
grep -Fq '"outcome":"passed"' "$scenario_root/recovery-check.json"
launch_count=$(wc -l <"$recovery_marker" | tr -d ' ')
if [ "$launch_count" -ne 1 ]
then
  printf 'recovered local check launched %s children; wanted exactly one\n' "$launch_count" >&2
  exit 1
fi

"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events.json"
grep -Fq '"type":"check.run_runtime_observed"' "$scenario_root/events.json"
grep -Fq '"type":"check.result_recorded"' "$scenario_root/events.json"
grep -Fq '"type":"check.freshness_stale"' "$scenario_root/events.json"
grep -Fq '"type":"check.repair_accepted"' "$scenario_root/events.json"
grep -Fq '"type":"check.watch_completed"' "$scenario_root/events.json"

stop_daemon
printf 'Owner-granted local checks and arbitrary-role check-watch acceptance: PASS\n'
