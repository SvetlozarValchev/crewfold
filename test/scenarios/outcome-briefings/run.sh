#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-outcome-briefings.XXXXXX")
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
    printf 'owner outcome and briefing acceptance failed; diagnostics follow\n' >&2
    if [ -f "$daemon_log" ]
    then
      tail -n 240 "$daemon_log" >&2
    fi
    for diagnostic in \
      "$scenario_root/briefing-before.json" \
      "$scenario_root/briefing-since.json" \
      "$scenario_root/explanation.json" \
      "$scenario_root/accepted-before.json" \
      "$scenario_root/old-after-restart.json" \
      "$scenario_root/new-after-restart.json"
    do
      if [ -f "$diagnostic" ]
      then
        printf '%s\n' "$diagnostic" >&2
        sed -n '1,80p' "$diagnostic" >&2
      fi
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

extract_id() {
  prefix=$1
  path=$2
  sed -n "s/.*\"id\":\"\(${prefix}_[0-9a-f]*\)\".*/\1/p" "$path" | sed -n '1p'
}

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

render_proposal() {
  template=$1
  commitment=$2
  handoff=$3
  output=$4
  sed -e "s/__COMMITMENT_ID__/$commitment/g" -e "s/__HANDOFF_ID__/$handoff/g" "$template" >"$output"
}

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -trimpath -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >/dev/null

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key outcome-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add demo --workspace personal --repo "$fixture_root/world-engine" --mode exclusive --socket "$socket_path" --idempotency-key outcome-project --output json >"$scenario_root/project.json"
checkout_id=$(extract_id co "$scenario_root/project.json")
"$binary" agent create implementer --workspace personal --role 'fixture change producer' --provider fake --runtime fake --max-concurrency 3 --socket "$socket_path" --idempotency-key outcome-agent --output json >"$scenario_root/agent.json"
"$binary" objective create 'Understand accepted delivery' --workspace personal --project demo --budget-tokens 10000 --budget-cents 1000 --budget-seconds 10000 --socket "$socket_path" --idempotency-key outcome-objective --output json >"$scenario_root/objective.json"
objective_id=$(extract_id obj "$scenario_root/objective.json")

# Ten transcript-free task histories prove the projection does not depend on a
# provider session. Every promise is created while its task is still pre-work.
index=1
while [ "$index" -le 10 ]
do
  "$binary" task create --workspace personal --project demo --objective "$objective_id" --title "Outcome history $index" --priority "$index" --socket "$socket_path" --idempotency-key "outcome-task-$index" --output json >"$scenario_root/task-$index.json"
  task_id=$(extract_id task "$scenario_root/task-$index.json")
  "$binary" outcome commitment add "deliverable-$index" --task "$task_id" --title "Promised deliverable $index" --criterion "Owner criterion $index" --workspace personal --socket "$socket_path" --idempotency-key "outcome-commitment-$index" --output json >"$scenario_root/commitment-$index.json"
  index=$((index + 1))
done

task_one=$(extract_id task "$scenario_root/task-1.json")
task_two=$(extract_id task "$scenario_root/task-2.json")
task_three=$(extract_id task "$scenario_root/task-3.json")
task_four=$(extract_id task "$scenario_root/task-4.json")
commitment_one=$(extract_id outcommit "$scenario_root/commitment-1.json")
commitment_two=$(extract_id outcommit "$scenario_root/commitment-2.json")
commitment_three=$(extract_id outcommit "$scenario_root/commitment-3.json")
commitment_four=$(extract_id outcommit "$scenario_root/commitment-4.json")

# Three completed fixture runs create exact self-report handoffs. The third
# remains only a proposed assessment, proving that even completed work plus a
# handoff does not become accepted delivery by implication.
for index in 1 2 3
do
  task_id=$(extract_id task "$scenario_root/task-$index.json")
  "$binary" task assign "$task_id" implementer --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key "outcome-assign-$index" --output json >"$scenario_root/assigned-$index.json"
  "$binary" run start "$task_id" --workspace personal --checkout "$checkout_id" --runtime fake --provider fake --scenario "$repo_root/test/fixtures/execution/success.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key "outcome-run-$index" --output json >"$scenario_root/run-$index.json"
  run_id=$(extract_id run "$scenario_root/run-$index.json")
  "$binary" run watch "$run_id" --workspace personal --wait-seconds 5 --socket "$socket_path" --output json >"$scenario_root/run-final-$index.json"
  grep -Fq '"status":"completed"' "$scenario_root/run-final-$index.json"
  grep -Fq '"handoff":{' "$scenario_root/run-final-$index.json"
done
handoff_one=$(extract_id handoff "$scenario_root/run-final-1.json")
handoff_two=$(extract_id handoff "$scenario_root/run-final-2.json")
handoff_three=$(extract_id handoff "$scenario_root/run-final-3.json")

render_proposal "$repo_root/test/fixtures/outcome-briefings/partial.yaml.in" "$commitment_one" "$handoff_one" "$scenario_root/partial.yaml"
"$binary" outcome propose --task "$task_one" "$scenario_root/partial.yaml" --workspace personal --socket "$socket_path" --idempotency-key partial-propose --output json >"$scenario_root/partial-proposed.json"
partial_id=$(extract_id outassess "$scenario_root/partial-proposed.json")
"$binary" outcome accept "$partial_id" --expected-state-revision 1 --decision-note 'Owner accepts explicit partial delivery' --workspace personal --socket "$socket_path" --idempotency-key partial-accept --output json >"$scenario_root/partial-accepted.json"
grep -Fq '"review_state":"accepted"' "$scenario_root/partial-accepted.json"
grep -Fq '"conclusion":"partial"' "$scenario_root/partial-accepted.json"

render_proposal "$repo_root/test/fixtures/outcome-briefings/achieved-with-current-exceptions.yaml.in" "$commitment_two" "$handoff_two" "$scenario_root/achieved-two.yaml"
"$binary" outcome propose --task "$task_two" "$scenario_root/achieved-two.yaml" --workspace personal --socket "$socket_path" --idempotency-key achieved-two-propose --output json >"$scenario_root/achieved-two-proposed.json"
achieved_two_id=$(extract_id outassess "$scenario_root/achieved-two-proposed.json")
"$binary" outcome accept "$achieved_two_id" --expected-state-revision 1 --workspace personal --socket "$socket_path" --idempotency-key achieved-two-accept --output json >"$scenario_root/achieved-two-accepted.json"

# A proposal is owner-authored but still not accepted delivery.
render_proposal "$repo_root/test/fixtures/outcome-briefings/achieved.yaml.in" "$commitment_three" "$handoff_three" "$scenario_root/proposed-three.yaml"
"$binary" outcome propose --task "$task_three" "$scenario_root/proposed-three.yaml" --workspace personal --socket "$socket_path" --idempotency-key proposed-three --output json >"$scenario_root/proposed-three.json"
proposed_three_id=$(extract_id outassess "$scenario_root/proposed-three.json")

# Rejection preserves the existing current truth and records no accepted result.
render_proposal "$repo_root/test/fixtures/outcome-briefings/unknown.yaml.in" "$commitment_four" unused "$scenario_root/unknown-four.yaml"
"$binary" outcome propose --task "$task_four" "$scenario_root/unknown-four.yaml" --workspace personal --socket "$socket_path" --idempotency-key unknown-four-propose --output json >"$scenario_root/unknown-four-proposed.json"
unknown_four_id=$(extract_id outassess "$scenario_root/unknown-four-proposed.json")
"$binary" outcome reject "$unknown_four_id" --expected-state-revision 1 --decision-note 'Owner rejects an unsupported judgment' --workspace personal --socket "$socket_path" --idempotency-key unknown-four-reject --output json >"$scenario_root/unknown-four-rejected.json"
grep -Fq '"review_state":"rejected"' "$scenario_root/unknown-four-rejected.json"

"$binary" outcome list --project demo --review-state accepted --workspace personal --socket "$socket_path" --output json >"$scenario_root/accepted-before.json"
accepted_before=$(grep -o '"review_state":"accepted"' "$scenario_root/accepted-before.json" | wc -l | tr -d ' ')
if [ "$accepted_before" -ne 2 ]
then
  printf 'accepted assessment count before successor=%s, want 2\n' "$accepted_before" >&2
  exit 1
fi
if grep -Fq "$proposed_three_id" "$scenario_root/accepted-before.json" || grep -Fq "$unknown_four_id" "$scenario_root/accepted-before.json"
then
  printf 'proposed or rejected assessment appeared in accepted outcome list\n' >&2
  exit 1
fi

"$binary" briefing show --project demo --workspace personal --socket "$socket_path" --output json >"$scenario_root/briefing-before.json"
grep -Fq '"kind":"accepted_delivery"' "$scenario_root/briefing-before.json"
grep -Fq '"review_state":"proposed"' "$scenario_root/proposed-three.json"

"$binary" checkpoint create --project demo --workspace personal --socket "$socket_path" --idempotency-key project-checkpoint --output json >"$scenario_root/checkpoint.json"
checkpoint_id=$(extract_id outcpnt "$scenario_root/checkpoint.json")
checkpoint_sequence=$(sed -n 's/.*"event_sequence":\([0-9][0-9]*\).*/\1/p' "$scenario_root/checkpoint.json" | sed -n '1p')
"$binary" checkpoint show "$checkpoint_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/checkpoint-show.json"
grep -Fq "\"id\":\"$checkpoint_id\"" "$scenario_root/checkpoint-show.json"
"$binary" checkpoint list --project demo --workspace personal --socket "$socket_path" --output json >"$scenario_root/checkpoint-list.json"
grep -Fq "\"id\":\"$checkpoint_id\"" "$scenario_root/checkpoint-list.json"

# A successor must cite the exact accepted assessment. Accepting it supersedes
# the old current assessment atomically.
render_proposal "$repo_root/test/fixtures/outcome-briefings/achieved.yaml.in" "$commitment_one" "$handoff_one" "$scenario_root/successor.yaml"
"$binary" outcome propose --task "$task_one" "$scenario_root/successor.yaml" --supersedes "$partial_id" --workspace personal --socket "$socket_path" --idempotency-key successor-propose --output json >"$scenario_root/successor-proposed.json"
successor_id=$(extract_id outassess "$scenario_root/successor-proposed.json")
"$binary" outcome accept "$successor_id" --expected-state-revision 1 --decision-note 'Owner accepts the exact successor' --workspace personal --socket "$socket_path" --idempotency-key successor-accept --output json >"$scenario_root/successor-accepted.json"

stop_daemon
start_daemon
"$binary" outcome show "$partial_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/old-after-restart.json"
"$binary" outcome show "$successor_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/new-after-restart.json"
grep -Fq '"review_state":"superseded"' "$scenario_root/old-after-restart.json"
grep -Fq '"review_state":"accepted"' "$scenario_root/new-after-restart.json"

"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events-before-briefing.json"
"$binary" briefing show --project demo --since "$checkpoint_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/briefing-since.json"
grep -Fq "\"checkpoint_id\":\"$checkpoint_id\"" "$scenario_root/briefing-since.json"
grep -Fq '"kind":"accepted_delivery"' "$scenario_root/briefing-since.json"
grep -Fq "$achieved_two_id" "$scenario_root/briefing-since.json"
grep -Fq 'Current accepted delivery still needs operator-scale validation' "$scenario_root/briefing-since.json"
grep -Fq 'Current accepted delivery has not yet been exercised through the M19 dashboard' "$scenario_root/briefing-since.json"
if grep -Fq 'Owner review load remains visible' "$scenario_root/briefing-since.json" || grep -Fq 'Large-project operator ergonomics require M19 validation' "$scenario_root/briefing-since.json"
then
  printf 'superseded assessment exceptions remained current in the briefing\n' >&2
  exit 1
fi

briefing_id=$(extract_id briefing "$scenario_root/briefing-since.json")
claim_id=$(extract_id bclaim "$scenario_root/briefing-since.json")
claim_count=$(grep -o '"id":"bclaim_[0-9a-f]*"' "$scenario_root/briefing-since.json" | wc -l | tr -d ' ')
byte_size=$(sed -n 's/.*"byte_size":\([0-9][0-9]*\).*/\1/p' "$scenario_root/briefing-since.json" | sed -n '1p')
if [ "$claim_count" -lt 1 ] || [ "$claim_count" -gt 128 ] || [ "$byte_size" -gt 65536 ]
then
  printf 'briefing bounds claims=%s bytes=%s\n' "$claim_count" "$byte_size" >&2
  exit 1
fi

# Every change-history claim must be strictly newer than the checkpoint, while
# current-state accepted delivery, risk, and unknown claims remain visible while
# superseded exceptions are represented only through change history.
sed -n 's/.*"kind":"change".*"source_event_sequence":\([0-9][0-9]*\).*/\1/p' "$scenario_root/briefing-since.json" | while IFS= read -r sequence
do
  if [ "$sequence" -le "$checkpoint_sequence" ]
  then
    printf 'change claim event %s is not newer than checkpoint %s\n' "$sequence" "$checkpoint_sequence" >&2
    exit 1
  fi
done

"$binary" briefing explain "$claim_id" --briefing "$briefing_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/explanation.json"
grep -Fq "\"briefing_id\":\"$briefing_id\"" "$scenario_root/explanation.json"
grep -Fq "\"id\":\"$claim_id\"" "$scenario_root/explanation.json"
grep -Fq '"provenance":[' "$scenario_root/explanation.json"

"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events-after-briefing.json"
if ! cmp -s "$scenario_root/events-before-briefing.json" "$scenario_root/events-after-briefing.json"
then
  printf 'briefing show/explain appended a fact event\n' >&2
  exit 1
fi

stop_daemon
printf 'Owner outcome and bounded briefing acceptance: PASS\n'
