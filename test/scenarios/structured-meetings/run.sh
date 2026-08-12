#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-structured-meetings.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
fixture_root="$scenario_root/git-fixture"
daemon_log="$scenario_root/daemon.log"
daemon_pid=""

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'structured meeting acceptance failed; collected diagnostics follow\n' >&2
    find "$scenario_root" -maxdepth 1 -type f -name '*.json' -o -name '*.log' | sort | while read -r diagnostic
    do
      printf '%s\n' "$diagnostic" >&2
      sed -n '1,240p' "$diagnostic" >&2
    done
  fi
  if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    kill "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
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
  while ! "$binary" status --socket "$socket_path" --output json >"$scenario_root/status.json" 2>/dev/null
  do
    if ! kill -0 "$daemon_pid" 2>/dev/null
    then
      wait "$daemon_pid" || true
      sed -n '1,240p' "$daemon_log" >&2
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
  "$binary" daemon stop --socket "$socket_path" --output json >"$scenario_root/stop.json"
  wait "$daemon_pid"
  daemon_pid=""
}

extract_id() {
  prefix=$1
  path=$2
  sed -n "s/.*\"id\":\"\(${prefix}_[0-9a-f]*\)\".*/\1/p" "$path" | sed -n '1p'
}

create_task() {
  key=$1
  title=$2
  path="$scenario_root/$key.json"
  "$binary" task create --workspace personal --project demo --title "$title" --socket "$socket_path" --idempotency-key "$key" --output json >"$path"
  extract_id task "$path"
}

add_overlap() {
  label=$1
  first_task=$2
  second_task=$3
  target=$4
  "$binary" claim add "$first_task" --workspace personal --project demo --checkout "$checkout_id" --write "$target/**" --mode exclusive --policy pause_scheduling --lease 1h --socket "$socket_path" --idempotency-key "$label-first-claim" --output json >"$scenario_root/$label-first-claim.json"
  "$binary" claim add "$second_task" --workspace personal --project demo --checkout "$checkout_id" --write "$target/item.go" --mode exclusive --policy request_resolution --lease 1h --socket "$socket_path" --idempotency-key "$label-second-claim" --output json >"$scenario_root/$label-second-claim.json"
  extract_id overlap "$scenario_root/$label-second-claim.json"
}

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key meeting-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add demo --workspace personal --repo "$fixture_root/world-engine" --mode exclusive --socket "$socket_path" --idempotency-key meeting-project --output json >"$scenario_root/project.json"
checkout_id=$(extract_id co "$scenario_root/project.json")

for name in agent-a agent-b agent-c manager decision-reviewer
do
  "$binary" agent create "$name" --workspace personal --role coordinator --provider fake --runtime fake --socket "$socket_path" --idempotency-key "create-$name" --output json >"$scenario_root/$name.json"
done
agent_a=$(extract_id agent "$scenario_root/agent-a.json")
agent_b=$(extract_id agent "$scenario_root/agent-b.json")
agent_c=$(extract_id agent "$scenario_root/agent-c.json")
manager=$(extract_id agent "$scenario_root/manager.json")
reviewer=$(extract_id agent "$scenario_root/decision-reviewer.json")

first_task=$(create_task owner-first-task "Own the shared contract")
second_task=$(create_task owner-second-task "Consume the shared contract")
owner_overlap=$(add_overlap owner "$first_task" "$second_task" internal/meeting-owner)
"$binary" meeting create --from-overlap "$owner_overlap" --participant "$agent_a" --participant "$agent_b" --facilitator "$manager" --policy owner_decision --timeout 1h --workspace personal --socket "$socket_path" --idempotency-key owner-meeting --output json >"$scenario_root/owner-created.json"
owner_meeting=$(extract_id meet "$scenario_root/owner-created.json")
sed -e "s/__AGENT_A__/$agent_a/g" -e "s/__AGENT_B__/$agent_b/g" "$repo_root/test/scenarios/structured-meetings/two-positions.json.in" >"$scenario_root/two-positions.json"
"$binary" meeting run "$owner_meeting" --fixture "$scenario_root/two-positions.json" --expected-revision 1 --workspace personal --socket "$socket_path" --idempotency-key owner-positions --output json >"$scenario_root/owner-positions.json"
grep -Fq '"status":"facilitator_pending"' "$scenario_root/owner-positions.json"
grep -Fq '"revision":2' "$scenario_root/owner-positions.json"

stop_daemon
start_daemon
sed -e "s/__BEFORE_TASK__/$first_task/g" -e "s/__AFTER_TASK__/$second_task/g" "$repo_root/test/scenarios/structured-meetings/sequence-proposal.json.in" >"$scenario_root/owner-proposal-fixture.json"
"$binary" meeting run "$owner_meeting" --fixture "$scenario_root/owner-proposal-fixture.json" --expected-revision 2 --workspace personal --socket "$socket_path" --idempotency-key owner-proposal --output json >"$scenario_root/owner-proposed.json"
grep -Fq '"status":"awaiting_approval"' "$scenario_root/owner-proposed.json"
position_count=$(grep -o '"round":"position"' "$scenario_root/owner-proposed.json" | wc -l)
if [ "$position_count" -ne 2 ]
then
  printf 'position count after restart=%s, want 2\n' "$position_count" >&2
  exit 1
fi
"$binary" task show "$second_task" --workspace personal --socket "$socket_path" --output json >"$scenario_root/task-before-owner-accept.json"
if grep -Fq '"depends_on_task_id"' "$scenario_root/task-before-owner-accept.json"
then
  printf 'owner-gated proposal mutated task before acceptance\n' >&2
  exit 1
fi
"$binary" meeting accept "$owner_meeting" --expected-revision 3 --note "owner approves sequencing" --workspace personal --socket "$socket_path" --idempotency-key owner-accept --output json >"$scenario_root/owner-accepted.json"
grep -Fq '"status":"concluded"' "$scenario_root/owner-accepted.json"
grep -Fq '"status":"applied"' "$scenario_root/owner-accepted.json"
"$binary" task show "$second_task" --workspace personal --socket "$socket_path" --output json >"$scenario_root/task-after-owner-accept.json"
grep -Fq "\"depends_on_task_id\":\"$first_task\"" "$scenario_root/task-after-owner-accept.json"
"$binary" overlap inspect "$owner_overlap" --workspace personal --socket "$socket_path" --output json >"$scenario_root/owner-overlap-after.json"
grep -Fq '"status":"resolved"' "$scenario_root/owner-overlap-after.json"

role_first=$(create_task role-first-task "Implement the versioned interface")
role_second=$(create_task role-second-task "Adapt to the versioned interface")
role_overlap=$(add_overlap roles "$role_first" "$role_second" internal/meeting-roles)
"$binary" meeting create --from-overlap "$role_overlap" --participant "$agent_a" --participant "$agent_b" --participant "$agent_c" --facilitator "$manager" --policy named_reviewer --reviewer "$reviewer" --timeout 1h --workspace personal --socket "$socket_path" --idempotency-key role-meeting --output json >"$scenario_root/role-created.json"
role_meeting=$(extract_id meet "$scenario_root/role-created.json")
sed -e "s/__AGENT_A__/$agent_a/g" -e "s/__AGENT_B__/$agent_b/g" -e "s/__AGENT_C__/$agent_c/g" -e "s/__TASK__/$role_first/g" "$repo_root/test/scenarios/structured-meetings/three-agent-review.json.in" >"$scenario_root/three-agent-review.json"
"$binary" meeting run "$role_meeting" --fixture "$scenario_root/three-agent-review.json" --expected-revision 1 --workspace personal --socket "$socket_path" --idempotency-key role-run --output json >"$scenario_root/role-concluded.json"
grep -Fq '"status":"concluded"' "$scenario_root/role-concluded.json"
grep -Fq '"role":"implementer"' "$scenario_root/role-concluded.json"
grep -Fq '"role":"reviewer"' "$scenario_root/role-concluded.json"
applied_count=$(grep -o '"status":"applied"' "$scenario_root/role-concluded.json" | wc -l)
if [ "$applied_count" -ne 2 ]
then
  printf 'applied role action count=%s, want 2\n' "$applied_count" >&2
  exit 1
fi

stalled_first=$(create_task stalled-first-task "Own the stalled prerequisite")
stalled_second=$(create_task stalled-second-task "Wait for the stalled prerequisite")
stalled_overlap=$(add_overlap stalled "$stalled_first" "$stalled_second" internal/meeting-stalled)
"$binary" meeting create --from-overlap "$stalled_overlap" --participant "$agent_a" --participant "$agent_b" --facilitator "$manager" --policy owner_decision --timeout 1h --workspace personal --socket "$socket_path" --idempotency-key stalled-meeting --output json >"$scenario_root/stalled-created.json"
stalled_meeting=$(extract_id meet "$scenario_root/stalled-created.json")
sed -e "s/__AGENT_A__/$agent_a/g" "$repo_root/test/scenarios/structured-meetings/one-position.json.in" >"$scenario_root/one-position.json"
"$binary" meeting run "$stalled_meeting" --fixture "$scenario_root/one-position.json" --expected-revision 1 --workspace personal --socket "$socket_path" --idempotency-key stalled-run --output json >"$scenario_root/stalled-run.json"
grep -Fq '"status":"stalled"' "$scenario_root/stalled-run.json"
grep -Fq '"status":"missing"' "$scenario_root/stalled-run.json"
grep -Fq '"summary":"finish the prerequisite first"' "$scenario_root/stalled-run.json"
sed -e "s/__BEFORE_TASK__/$stalled_first/g" -e "s/__AFTER_TASK__/$stalled_second/g" "$repo_root/test/scenarios/structured-meetings/sequence-proposal.json.in" >"$scenario_root/takeover-wrapper.json"
sed -n '/"proposal"/,$p' "$scenario_root/takeover-wrapper.json" | sed '1s/  "proposal": //' | sed '$d' >"$scenario_root/takeover-proposal.json"
"$binary" meeting takeover "$stalled_meeting" --proposal "$scenario_root/takeover-proposal.json" --expected-revision 2 --note "owner resolves missing participant" --workspace personal --socket "$socket_path" --idempotency-key stalled-takeover --output json >"$scenario_root/stalled-takeover.json"
grep -Fq '"status":"concluded"' "$scenario_root/stalled-takeover.json"
grep -Fq '"type":"meeting_mutation"' "$scenario_root/stalled-takeover.json"

"$binary" events list --after 0 --limit 500 --socket "$socket_path" --output json >"$scenario_root/events.json"
grep -Fq '"type":"meeting.created"' "$scenario_root/events.json"
grep -Fq '"type":"meeting.concluded"' "$scenario_root/events.json"
grep -Fq '"type":"meeting.human_takeover"' "$scenario_root/events.json"

stop_daemon
printf 'Structured meeting acceptance: PASS\n'
