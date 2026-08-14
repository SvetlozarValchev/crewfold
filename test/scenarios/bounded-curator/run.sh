#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-bounded-curator.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
fixture_root="$scenario_root/git-fixture"
daemon_log="$scenario_root/daemon.log"
daemon_pid=''

process_start_identity() {
  pid=$1
  [ -r "/proc/$pid/stat" ] || return
  sed 's/.*) //' "/proc/$pid/stat" | awk '{print $20}'
}

kill_owned_runtime_process() {
  pid=$1
  expected_start=$2
  if [ -z "$pid" ] || [ "$pid" -le 1 ] || [ -z "$expected_start" ] ||
     ! kill -0 "$pid" 2>/dev/null || [ ! -r "/proc/$pid/cmdline" ] ||
     [ "$(process_start_identity "$pid")" != "$expected_start" ]
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
    [ -f "$state_path" ] || continue
    for field in supervisor_pid child_pid
    do
      pid=$(sed -n "s/.*\"$field\":\([0-9][0-9]*\).*/\1/p" "$state_path")
      case "$field" in
        supervisor_pid) start_field=supervisor_start ;;
        child_pid) start_field=child_start ;;
      esac
      start=$(sed -n "s/.*\"$start_field\":\"\([^\"]*\)\".*/\1/p" "$state_path")
      kill_owned_runtime_process "$pid" "$start"
    done
  done
}

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'Bounded curator acceptance failed; diagnostics follow\n' >&2
    find "$scenario_root" -maxdepth 1 -type f \( -name '*.json' -o -name '*.log' \) -print 2>/dev/null | sort | while IFS= read -r diagnostic
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
  cleanup_runtime_processes
  if [ -d "$scenario_root" ]
  then
    find "$scenario_root" -depth -delete
  fi
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

assert_contains() {
  path=$1
  expected=$2
  message=$3
  grep -Fq "$expected" "$path" || fail "$message"
}

assert_absent() {
  path=$1
  unexpected=$2
  message=$3
  if grep -Fq "$unexpected" "$path"
  then
    fail "$message"
  fi
}

assert_count() {
  path=$1
  pattern=$2
  expected=$3
  message=$4
  actual=$( (grep -o "$pattern" "$path" || true) | wc -l | tr -d ' ')
  if [ "$actual" -ne "$expected" ]
  then
    fail "$message (got $actual, want $expected)"
  fi
}

extract_id() {
  prefix=$1
  path=$2
  sed -n "s/.*\"id\":\"\(${prefix}_[0-9a-f]*\)\".*/\1/p" "$path" | sed -n '1p'
}

record_with_id() {
  path=$1
  prefix=$2
  id=$3
  suffix=$(printf '%s\n' "$id" | sed "s/^${prefix}_//")
  awk -v separator="\"id\":\"${prefix}_" -v suffix="$suffix" '
    BEGIN { RS = separator }
    NR > 1 && substr($0, 1, 32) == suffix {
      print separator $0
      exit
    }
  ' "$path"
}

record_containing() {
  path=$1
  prefix=$2
  needle=$3
  awk -v separator="\"id\":\"${prefix}_" -v needle="$needle" '
    BEGIN { RS = separator }
    NR > 1 && index($0, needle) > 0 {
      print separator $0
      exit
    }
  ' "$path"
}

json_string_field() {
  record=$1
  field=$2
  printf '%s\n' "$record" | sed -n "s/.*\"$field\":\"\([^\"]*\)\".*/\1/p"
}

json_number_field() {
  record=$1
  field=$2
  printf '%s\n' "$record" | sed -n "s/.*\"$field\":\([0-9][0-9]*\).*/\1/p"
}

assert_event_link() {
  path=$1
  sequence=$2
  event_type=$3
  entity_type=$4
  entity_id=$5
  message=$6
  if ! awk -v sequence="$sequence" -v event_type="$event_type" -v entity_type="$entity_type" -v entity_id="$entity_id" '
    BEGIN { RS = "\"event_id\":\"" }
    NR > 1 &&
      index($0, "\"sequence\":" sequence ",") > 0 &&
      index($0, "\"type\":\"" event_type "\"") > 0 &&
      index($0, "\"entity\":{\"type\":\"" entity_type "\",\"id\":\"" entity_id "\",") > 0 {
        found = 1
      }
    END { exit !found }
  ' "$path"
  then
    fail "$message"
  fi
}

revision_for_source() {
  path=$1
  source_id=$2
  awk -v source="\"id\":\"$source_id\"" '
    BEGIN { RS = "\\\"id\\\":\\\"krev_" }
    NR > 1 && index($0, source) > 0 {
      print "krev_" substr($0, 1, 32)
      exit
    }
  ' "$path"
}

start_daemon() {
  "$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" 2>>"$daemon_log" &
  daemon_pid=$!
  attempts=0
  until "$binary" status --socket "$socket_path" --output json >"$scenario_root/status.json" 2>/dev/null
  do
    if ! kill -0 "$daemon_pid" 2>/dev/null
    then
      wait "$daemon_pid" || true
      fail 'daemon exited before readiness'
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 300 ]
    then
      fail 'timed out waiting for daemon readiness'
    fi
    sleep 0.01
  done
}

stop_daemon() {
  label=$1
  "$binary" daemon stop --socket "$socket_path" --output json >"$scenario_root/$label-stop.json"
  wait "$daemon_pid"
  daemon_pid=''
  [ ! -e "$socket_path" ] || fail "socket remained after $label daemon stop"
}

create_task() {
  label=$1
  title=$2
  "$binary" task create --workspace personal --project demo --title "$title" \
    --socket "$socket_path" --idempotency-key "$label" --output json >"$scenario_root/$label.json"
  extract_id task "$scenario_root/$label.json"
}

create_accepted_resolution() {
  index=$1
  supplied_summary=${2:-}
  before_task=$(create_task "curator-before-$index" "Publish curator contract $index")
  after_task=$(create_task "curator-after-$index" "Consume curator contract $index")
  [ -n "$before_task" ] && [ -n "$after_task" ] || fail "resolution $index task ID is missing"
  target="internal/curator-resolution-$index"
  "$binary" claim add "$before_task" --workspace personal --project demo --checkout "$checkout_id" \
    --write "$target/**" --mode exclusive --policy pause_scheduling --lease 1h \
    --socket "$socket_path" --idempotency-key "curator-claim-before-$index" --output json >"$scenario_root/claim-before-$index.json"
  "$binary" claim add "$after_task" --workspace personal --project demo --checkout "$checkout_id" \
    --write "$target/contract.go" --mode exclusive --policy request_resolution --lease 1h \
    --socket "$socket_path" --idempotency-key "curator-claim-after-$index" --output json >"$scenario_root/claim-after-$index.json"
  overlap_id=$(extract_id overlap "$scenario_root/claim-after-$index.json")
  [ -n "$overlap_id" ] || fail "resolution $index overlap ID is missing"
  "$binary" meeting create --from-overlap "$overlap_id" --participant "$agent_a" --participant "$agent_b" \
    --facilitator "$manager" --policy owner_decision --timeout 1h --workspace personal \
    --socket "$socket_path" --idempotency-key "curator-meeting-$index" --output json >"$scenario_root/meeting-created-$index.json"
  meeting_id=$(extract_id meet "$scenario_root/meeting-created-$index.json")
  [ -n "$meeting_id" ] || fail "resolution $index meeting ID is missing"
  summary=$supplied_summary
  if [ -z "$summary" ]
  then
    summary="Curator accepted resolution $index"
  fi
  sed -e "s/__AGENT_A__/$agent_a/g" -e "s/__AGENT_B__/$agent_b/g" \
    -e "s/__BEFORE_TASK__/$before_task/g" -e "s/__AFTER_TASK__/$after_task/g" \
    -e "s/__SUMMARY__/$summary/g" "$repo_root/test/scenarios/bounded-curator/resolution.json.in" \
    >"$scenario_root/meeting-fixture-$index.json"
  "$binary" meeting run "$meeting_id" --fixture "$scenario_root/meeting-fixture-$index.json" \
    --expected-revision 1 --workspace personal --socket "$socket_path" \
    --idempotency-key "curator-meeting-run-$index" --output json >"$scenario_root/meeting-proposed-$index.json"
  assert_contains "$scenario_root/meeting-proposed-$index.json" '"status":"awaiting_approval"' "resolution $index did not await owner approval"
  "$binary" meeting accept "$meeting_id" --expected-revision 2 --note "Accept structured curator resolution $index" \
    --workspace personal --socket "$socket_path" --idempotency-key "curator-meeting-accept-$index" \
    --output json >"$scenario_root/meeting-accepted-$index.json"
  assert_contains "$scenario_root/meeting-accepted-$index.json" '"status":"concluded"' "resolution $index meeting was not concluded"
  assert_contains "$scenario_root/meeting-accepted-$index.json" '"status":"accepted"' "resolution $index proposal was not accepted"
}

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >/dev/null

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key curator-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add demo --workspace personal --repo "$fixture_root/world-engine" --mode exclusive \
  --socket "$socket_path" --idempotency-key curator-project --output json >"$scenario_root/project.json"
checkout_id=$(extract_id co "$scenario_root/project.json")
[ -n "$checkout_id" ] || fail 'curator fixture checkout ID is missing'

for name in meeting-agent-a meeting-agent-b meeting-manager proposal-agent
do
  "$binary" agent create "$name" --workspace personal --role coordinator --provider fixture-mcp --runtime direct \
    --socket "$socket_path" --idempotency-key "curator-agent-$name" --output json >"$scenario_root/$name.json"
done
agent_a=$(extract_id agent "$scenario_root/meeting-agent-a.json")
agent_b=$(extract_id agent "$scenario_root/meeting-agent-b.json")
manager=$(extract_id agent "$scenario_root/meeting-manager.json")
[ -n "$agent_a" ] && [ -n "$agent_b" ] && [ -n "$manager" ] || fail 'meeting agent ID is missing'

index=1
while [ "$index" -le 11 ]
do
  create_accepted_resolution "$index"
  index=$((index + 1))
done

# Meeting summaries may be up to 4096 bytes, while the exact safe-copy rule is
# deliberately capped at 2048. This valid accepted source must be explained as a
# skip and must never be truncated into a knowledge proposal.
oversized_summary=$(awk 'BEGIN { for (i = 0; i < 2049; i++) printf "x" }')
create_accepted_resolution 12 "$oversized_summary"
oversized_proposal=$(extract_id proposal "$scenario_root/meeting-accepted-12.json")
[ -n "$oversized_proposal" ] || fail 'oversized accepted proposal ID is missing'

proposal_task=$(create_task curator-agent-proposal-task 'Propose an arbitrary high-confidence finding')
"$binary" task assign "$proposal_task" proposal-agent --lease-seconds 600 --workspace personal \
  --expected-revision 1 --socket "$socket_path" --idempotency-key curator-proposal-assignment \
  --output json >"$scenario_root/proposal-assigned.json"

if "$binary" run start "$proposal_task" --workspace personal --runtime direct --provider fixture-mcp \
  --scenario "$repo_root/test/fixtures/curator/forged-source.json" --expected-task-revision 2 \
  --socket "$socket_path" --idempotency-key curator-forged-run --output json >"$scenario_root/forged-source-error.json" 2>&1
then
  fail 'fixture schema allowed an agent to select a knowledge source'
fi
assert_contains "$scenario_root/forged-source-error.json" 'unknown field' 'forged source failed for an unexpected reason'

"$binary" context build "$proposal_task" --workspace personal --agent proposal-agent --expected-task-revision 2 \
  --socket "$socket_path" --idempotency-key curator-proposal-context --output json >"$scenario_root/proposal-context.json"
proposal_context=$(extract_id ctx "$scenario_root/proposal-context.json")
"$binary" run start "$proposal_task" --workspace personal --context "$proposal_context" --runtime direct --provider fixture-mcp \
  --scenario "$repo_root/test/fixtures/curator/agent-proposal.json" --expected-task-revision 2 \
  --socket "$socket_path" --idempotency-key curator-agent-run --output json >"$scenario_root/proposal-run.json"
proposal_run=$(extract_id run "$scenario_root/proposal-run.json")
"$binary" run watch "$proposal_run" --workspace personal --wait-seconds 15 --socket "$socket_path" \
  --output json >"$scenario_root/proposal-final.json"
assert_contains "$scenario_root/proposal-final.json" '"status":"completed"' 'agent proposal fixture did not complete'

"$binary" curator queue --workspace personal --project demo --limit 200 --socket "$socket_path" \
  --output json >"$scenario_root/queue-before-process.json"
assert_count "$scenario_root/queue-before-process.json" '"review_status":"proposed"' 1 'queue did not contain exactly the arbitrary agent proposal'
agent_revision=$(extract_id krev "$scenario_root/queue-before-process.json")
[ -n "$agent_revision" ] || fail 'agent proposal revision ID is missing'
assert_contains "$scenario_root/queue-before-process.json" '"eligibility":"manual_review"' 'agent proposal was not classified for manual review'
assert_contains "$scenario_root/queue-before-process.json" '"name":"accepted_meeting_resolution_copy/v1","revision":1,"enabled":false' 'queue omitted the persisted default-disabled rule revision'

# Rules are disabled by default. Processing still performs the one exact
# deterministic derivation, but cannot accept any resulting revision.
"$binary" curator process --workspace personal --project demo --socket "$socket_path" \
  --idempotency-key curator-disabled-process --output json >"$scenario_root/disabled-process.json"
assert_count "$scenario_root/disabled-process.json" '"id":"cder_' 11 'disabled process did not derive each accepted meeting exactly once'
assert_contains "$scenario_root/disabled-process.json" '"accepted":[]' 'disabled rule automatically accepted knowledge'
assert_count "$scenario_root/disabled-process.json" '"reason":"summary_not_exact_safe_copy"' 1 'oversized accepted summary did not report its exact stable skip reason'
assert_contains "$scenario_root/disabled-process.json" "\"source_type\":\"meeting_proposal\",\"source_id\":\"$oversized_proposal\",\"source_revision\":2,\"reason\":\"summary_not_exact_safe_copy\"" 'skip omitted the exact oversized proposal revision'
disabled_candidates=$(sed -n 's/.*"candidates_scanned":\([0-9][0-9]*\).*/\1/p' "$scenario_root/disabled-process.json")
[ -n "$disabled_candidates" ] && [ "$disabled_candidates" -le 100 ] || fail 'disabled process exceeded its 100-candidate bound'
disabled_derived_revision=$(grep -o '"knowledge_revision_id":"krev_[0-9a-f]*"' "$scenario_root/disabled-process.json" | sed -n '1s/.*"\(krev_[0-9a-f]*\)"/\1/p')
[ -n "$disabled_derived_revision" ] || fail 'disabled process omitted its exact derived revision ID'
"$binary" curator queue --workspace personal --project demo --limit 200 --socket "$socket_path" \
  --output json >"$scenario_root/queue-before-restart.json"
assert_count "$scenario_root/queue-before-restart.json" '"review_status":"proposed"' 12 'derived queue did not contain agent plus meeting proposals'
assert_count "$scenario_root/queue-before-restart.json" '"eligibility_reason":"rule_disabled"' 11 'disabled derived proposals did not explain the rule gate'
assert_count "$scenario_root/queue-before-restart.json" '"eligibility_reason":"not_curator_derived"' 1 'agent proposal did not retain its manual-review reason'

stop_daemon first
start_daemon
"$binary" curator queue --workspace personal --project demo --limit 200 --socket "$socket_path" \
  --output json >"$scenario_root/queue-after-restart.json"
cmp "$scenario_root/queue-before-restart.json" "$scenario_root/queue-after-restart.json" || fail 'curator queue changed across restart'
"$binary" curator process --workspace personal --project demo --socket "$socket_path" \
  --idempotency-key curator-disabled-process --output json >"$scenario_root/disabled-process-replay.json"
cmp "$scenario_root/disabled-process.json" "$scenario_root/disabled-process-replay.json" || fail 'curator process replay changed across restart'

"$binary" curator process --workspace personal --project demo --socket "$socket_path" \
  --idempotency-key curator-skipped-reevaluation --output json >"$scenario_root/skipped-reevaluation.json"
assert_contains "$scenario_root/skipped-reevaluation.json" '"derived":[]' 'fresh derive-only pass duplicated a derivation'
assert_contains "$scenario_root/skipped-reevaluation.json" '"accepted":[]' 'derive-only pass accepted knowledge'
assert_count "$scenario_root/skipped-reevaluation.json" '"reason":"summary_not_exact_safe_copy"' 1 'fresh pass did not repeat the deterministic skip evaluation'

"$binary" curator rule enable accepted-meeting-resolution-copy --workspace personal --expected-revision 1 \
  --socket "$socket_path" --idempotency-key curator-enable-rule --output json >"$scenario_root/rule-enabled.json"
assert_contains "$scenario_root/rule-enabled.json" '"enabled":true' 'owner did not enable the exact curator rule'
assert_contains "$scenario_root/rule-enabled.json" '"revision":2' 'first rule configuration did not advance the persisted default rule to revision 2'
"$binary" curator rule enable accepted-meeting-resolution-copy --workspace personal --expected-revision 1 \
  --socket "$socket_path" --idempotency-key curator-enable-rule --output json >"$scenario_root/rule-enabled-replay.json"
cmp "$scenario_root/rule-enabled.json" "$scenario_root/rule-enabled-replay.json" || fail 'curator rule replay changed'
"$binary" curator queue --workspace personal --project demo --limit 200 --socket "$socket_path" \
  --output json >"$scenario_root/queue-after-enable.json"
assert_contains "$scenario_root/queue-after-enable.json" '"name":"accepted_meeting_resolution_copy/v1","revision":2,"enabled":true' 'queue did not expose the enabled optimistic rule revision'
assert_count "$scenario_root/queue-after-enable.json" '"eligibility":"safe_auto_accept"' 11 'enabled rule did not make only exact derivations safely eligible'
assert_count "$scenario_root/queue-after-enable.json" '"eligibility":"manual_review"' 1 'enabled rule changed the arbitrary proposal eligibility'

"$binary" curator process --workspace personal --project demo --apply-safe --socket "$socket_path" \
  --idempotency-key curator-safe-process-one --output json >"$scenario_root/safe-process-one.json"
assert_count "$scenario_root/safe-process-one.json" '"id":"cauto_' 10 'one process pass did not enforce the ten-acceptance bound'
assert_count "$scenario_root/safe-process-one.json" '"reason":"summary_not_exact_safe_copy"' 1 'safe pass lost the oversized-source skip explanation'
safe_candidates=$(sed -n 's/.*"candidates_scanned":\([0-9][0-9]*\).*/\1/p' "$scenario_root/safe-process-one.json")
[ -n "$safe_candidates" ] && [ "$safe_candidates" -le 100 ] || fail 'safe process exceeded its 100-candidate bound'
assert_contains "$scenario_root/safe-process-one.json" '"derived":[]' 'enabled process duplicated existing derivations'
"$binary" curator process --workspace personal --project demo --apply-safe --socket "$socket_path" \
  --idempotency-key curator-safe-process-one --output json >"$scenario_root/safe-process-one-replay.json"
cmp "$scenario_root/safe-process-one.json" "$scenario_root/safe-process-one-replay.json" || fail 'safe curator process was not idempotent'
"$binary" curator queue --workspace personal --project demo --limit 200 --socket "$socket_path" \
  --output json >"$scenario_root/queue-after-ten.json"
assert_count "$scenario_root/queue-after-ten.json" '"review_status":"proposed"' 2 'first safe pass did not leave one eligible and one manual proposal'
assert_count "$scenario_root/queue-after-ten.json" '"eligibility":"safe_auto_accept"' 1 'remaining exact derivation is not safely eligible'
assert_count "$scenario_root/queue-after-ten.json" '"eligibility":"manual_review"' 1 'arbitrary agent proposal is not manual review'

"$binary" curator process --workspace personal --project demo --apply-safe --socket "$socket_path" \
  --idempotency-key curator-safe-process-two --output json >"$scenario_root/safe-process-two.json"
assert_count "$scenario_root/safe-process-two.json" '"id":"cauto_' 1 'second process pass did not accept the final eligible proposal'
assert_count "$scenario_root/safe-process-two.json" '"reason":"summary_not_exact_safe_copy"' 1 'second safe pass lost the oversized-source skip explanation'
"$binary" curator queue --workspace personal --project demo --limit 200 --socket "$socket_path" \
  --output json >"$scenario_root/final-queue.json"
assert_count "$scenario_root/final-queue.json" '"review_status":"proposed"' 1 'automatic processing changed the arbitrary agent proposal'
assert_contains "$scenario_root/final-queue.json" "\"id\":\"$agent_revision\"" 'agent proposal disappeared from the queue'
assert_contains "$scenario_root/final-queue.json" '"eligibility":"manual_review"' 'agent proposal gained safe eligibility'

"$binary" knowledge show "$agent_revision" --workspace personal --socket "$socket_path" --output json >"$scenario_root/agent-proposal-show.json"
assert_contains "$scenario_root/agent-proposal-show.json" '"confidence":"high"' 'agent proposal confidence changed'
assert_contains "$scenario_root/agent-proposal-show.json" '"verification_status":"verified"' 'agent proposal verification changed'
assert_contains "$scenario_root/agent-proposal-show.json" "\"type\":\"task\",\"id\":\"$proposal_task\"" 'agent proposal did not retain its real task source'
assert_contains "$scenario_root/agent-proposal-show.json" "\"proposed_by\":\"$proposal_run\",\"proposed_by_type\":\"agent_run\"" 'agent proposal actor was not bound to its run'
assert_contains "$scenario_root/agent-proposal-show.json" '"authority_checks":[]' 'reserved MCP denial reached or mutated knowledge governance'
assert_absent "$scenario_root/agent-proposal-show.json" '"review_status":"accepted"' 'agent proposal was automatically accepted'

"$binary" knowledge list --workspace personal --project demo --type decision --review-status accepted \
  --socket "$socket_path" --output json >"$scenario_root/accepted-decisions.json"
assert_count "$scenario_root/accepted-decisions.json" '"review_status":"accepted"' 11 'curator did not accept exactly eleven derived decisions'
assert_contains "$scenario_root/accepted-decisions.json" "\"id\":\"$disabled_derived_revision\"" 'enabled processing did not accept the exact revision derived while disabled'
assert_count "$scenario_root/accepted-decisions.json" '"type":"meeting_proposal"' 11 'derived decisions do not each have one meeting-proposal source'
assert_count "$scenario_root/accepted-decisions.json" '"role":"primary","ordinal":0' 11 'derived decisions have unexpected supporting provenance'
assert_count "$scenario_root/accepted-decisions.json" '"confidence":"medium"' 11 'derived confidence is not exact'
assert_count "$scenario_root/accepted-decisions.json" '"verification_status":"supported"' 11 'derived verification is not exact'
assert_count "$scenario_root/accepted-decisions.json" '"freshness_policy":"until_superseded"' 11 'derived freshness is not exact'
assert_absent "$scenario_root/accepted-decisions.json" '"task_scope_id"' 'derived decisions gained task scope'
assert_absent "$scenario_root/accepted-decisions.json" '"supersedes_revision_id"' 'derived decisions gained a predecessor'
assert_absent "$scenario_root/accepted-decisions.json" "$oversized_proposal" 'oversized accepted summary was truncated into knowledge'

index=1
while [ "$index" -le 11 ]
do
  agenda=$(sed -n 's/.*"agenda":"\([^"]*\)".*/\1/p' "$scenario_root/meeting-created-$index.json")
  proposal_id=$(extract_id proposal "$scenario_root/meeting-accepted-$index.json")
  [ -n "$agenda" ] && [ -n "$proposal_id" ] || fail "resolution $index frozen source metadata is missing"
  assert_contains "$scenario_root/accepted-decisions.json" "\"title\":\"$agenda\",\"body\":\"Curator accepted resolution $index\"" "resolution $index transform did not copy exact agenda and summary"
  assert_contains "$scenario_root/accepted-decisions.json" "\"type\":\"meeting_proposal\",\"id\":\"$proposal_id\",\"revision\":2,\"role\":\"primary\",\"ordinal\":0" "resolution $index did not freeze exact accepted proposal revision"
  index=$((index + 1))
done

first_proposal=$(extract_id proposal "$scenario_root/meeting-accepted-1.json")
first_revision=$(revision_for_source "$scenario_root/accepted-decisions.json" "$first_proposal")
[ -n "$first_revision" ] || fail 'could not associate the first source with its derived revision'
auto_record=$(record_containing "$scenario_root/safe-process-one.json" cauto "\"knowledge_revision_id\":\"$first_revision\"")
if [ -z "$auto_record" ]
then
  auto_record=$(record_containing "$scenario_root/safe-process-two.json" cauto "\"knowledge_revision_id\":\"$first_revision\"")
fi
[ -n "$auto_record" ] || fail 'automatic acceptance omitted the exact first derived revision link'
auto_id=$(printf '%s\n' "$auto_record" | sed -n 's/^"id":"\([^"]*\)".*/\1/p')
auto_derivation=$(json_string_field "$auto_record" derivation_id)
auto_authority=$(json_string_field "$auto_record" authority_check_id)
auto_knowledge_sequence=$(json_number_field "$auto_record" knowledge_event_sequence)
auto_event_sequence=$(json_number_field "$auto_record" event_sequence)
[ -n "$auto_id" ] && [ -n "$auto_derivation" ] && [ -n "$auto_authority" ] && \
  [ -n "$auto_knowledge_sequence" ] && [ -n "$auto_event_sequence" ] || fail 'automatic acceptance evidence is incomplete'
assert_contains "$scenario_root/safe-process-one.json" '"rule_name":"accepted_meeting_resolution_copy/v1","rule_revision":2' 'automatic acceptance omitted its enabled evaluation rule revision'

derivation_record=$(record_with_id "$scenario_root/disabled-process.json" cder "$auto_derivation")
[ -n "$derivation_record" ] || fail 'automatic acceptance does not link to the disabled-pass derivation'
derivation_knowledge=$(json_string_field "$derivation_record" knowledge_revision_id)
derivation_source=$(json_string_field "$derivation_record" source_id)
derivation_event_sequence=$(json_number_field "$derivation_record" event_sequence)
[ "$derivation_knowledge" = "$first_revision" ] || fail 'derivation and automatic acceptance point at different knowledge revisions'
[ "$derivation_source" = "$first_proposal" ] || fail 'derivation does not point at the exact accepted meeting proposal'
[ -n "$derivation_event_sequence" ] || fail 'derivation omitted its event sequence'
printf '%s\n' "$derivation_record" | grep -Fq '"rule_name":"accepted_meeting_resolution_copy/v1","rule_revision":1' || fail 'derivation omitted its disabled rule revision'

"$binary" knowledge show "$first_revision" --workspace personal --socket "$socket_path" --output json >"$scenario_root/derived-show.json"
assert_contains "$scenario_root/derived-show.json" '"actor":{"id":"subsystem:curator","type":"subsystem"}' 'curator authority actor is missing'
assert_contains "$scenario_root/derived-show.json" '"outcome":"allowed","reason":"state_policy"' 'curator state-policy authority is missing'
authority_record=$(record_with_id "$scenario_root/derived-show.json" kauth "$auto_authority")
[ -n "$authority_record" ] || fail 'automatic acceptance does not link to its exact authority check'
[ "$(json_string_field "$authority_record" revision_id)" = "$first_revision" ] || fail 'authority check points at a different knowledge revision'
[ "$(json_number_field "$authority_record" event_sequence)" = "$auto_knowledge_sequence" ] || fail 'authority check and automatic acceptance point at different knowledge events'

"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events.json"
assert_count "$scenario_root/events.json" '"type":"knowledge.proposed"' 12 'oversized source or retry created an unexpected knowledge proposal fact'
assert_count "$scenario_root/events.json" '"type":"curator.derived"' 11 'derivation event count is not exact'
assert_count "$scenario_root/events.json" '"type":"curator.auto_accepted"' 11 'automatic acceptance event count is not exact'
assert_count "$scenario_root/events.json" '"type":"knowledge.accepted"' 11 'normal knowledge acceptance event count is not exact'
assert_count "$scenario_root/events.json" '"type":"curator.rule_configured"' 1 'rule configuration event count is not exact'
assert_count "$scenario_root/events.json" '"type":"run.tool_denied"' 1 'reserved agent acceptance was not denied exactly once'
assert_count "$scenario_root/events.json" '"type":"run.requested"' 1 'invalid forged-source fixture created a run'
assert_absent "$scenario_root/events.json" '"type":"knowledge.rejected"' 'oversized or arbitrary knowledge was unexpectedly rejected'
assert_absent "$scenario_root/events.json" '"type":"knowledge.marked_stale"' 'oversized or arbitrary knowledge was unexpectedly marked stale'
assert_absent "$scenario_root/events.json" '"type":"knowledge.acceptance_denied"' 'reserved MCP denial improperly reached knowledge governance'
assert_event_link "$scenario_root/events.json" "$derivation_event_sequence" curator.derived curator_derivation "$auto_derivation" 'derivation evidence does not link to its exact curator event'
assert_event_link "$scenario_root/events.json" "$auto_knowledge_sequence" knowledge.accepted knowledge_revision "$first_revision" 'automatic acceptance does not link to the normal knowledge event'
assert_event_link "$scenario_root/events.json" "$auto_event_sequence" curator.auto_accepted curator_auto_acceptance "$auto_id" 'automatic acceptance evidence does not link to its exact curator event'
assert_contains "$scenario_root/events.json" "$first_revision" 'curator events omit the exact derived revision'
assert_contains "$scenario_root/events.json" "$first_proposal" 'curator events omit the exact source proposal'

stop_daemon final
printf 'Bounded deterministic curator acceptance: PASS\n'
