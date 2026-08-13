#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-knowledge-contradictions.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
fixture_root="$scenario_root/git-fixture"
daemon_log="$scenario_root/daemon.log"
daemon_pid=''
sentinel='CONTRADICTION_MCP_INPUT_SENTINEL'

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'Knowledge contradiction acceptance failed; diagnostics follow\n' >&2
    for diagnostic in \
      "$daemon_log" \
      "$scenario_root/report-fixture.json" \
      "$scenario_root/run-final.json" \
      "$scenario_root/run-logs.json" \
      "$scenario_root/proposed-show.json" \
      "$scenario_root/open.json" \
      "$scenario_root/open-show.json" \
      "$scenario_root/open-search.json" \
      "$scenario_root/context-conflict.json" \
      "$scenario_root/dismissed.json" \
      "$scenario_root/events.json"
    do
      if [ -f "$diagnostic" ]
      then
        printf '%s\n' "$diagnostic" >&2
        sed -n '1,240p' "$diagnostic" >&2
      fi
    done
  fi
  if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    kill "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  if [ -d "$data_dir/runtime" ]
  then
    find "$data_dir/runtime" -type f -name state.json | while IFS= read -r state_path
    do
      supervisor_pid=$(sed -n 's/.*"supervisor_pid":\([0-9][0-9]*\).*/\1/p' "$state_path")
      if [ -n "$supervisor_pid" ] && [ "$supervisor_pid" -gt 1 ] && kill -0 "$supervisor_pid" 2>/dev/null
      then
        kill -KILL "$supervisor_pid" 2>/dev/null || true
      fi
    done
  fi
  if [ -d "$scenario_root" ]
  then
    find "$scenario_root" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

extract_id() {
  prefix=$1
  path=$2
  sed -n "s/.*\"id\":\"\(${prefix}_[0-9a-f]*\)\".*/\1/p" "$path" | sed -n '1p'
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
  actual=$(grep -o "$pattern" "$path" | wc -l | tr -d ' ')
  [ "$actual" -eq "$expected" ] || fail "$message: got $actual, want $expected"
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
  name=$1
  "$binary" daemon stop --socket "$socket_path" --output json >"$scenario_root/$name-stop.json"
  wait "$daemon_pid"
  daemon_pid=''
  [ ! -e "$socket_path" ] || fail "socket remained after $name daemon stop"
}

create_task() {
  output=$1
  title=$2
  key=$3
  "$binary" task create --workspace personal --project demo --title "$title" \
    --socket "$socket_path" --idempotency-key "$key" --output json >"$output"
}

propose_decision() {
  output=$1
  source_task=$2
  title=$3
  body=$4
  key=$5
  shift 5
  markdown="$scenario_root/$key.md"
  {
    printf '# %s\n\n' "$title"
    printf '%s\n' "$body"
  } >"$markdown"
  "$binary" knowledge propose "$markdown" --workspace personal --type decision \
    --from-task "$source_task" --confidence high --verification verified \
    --socket "$socket_path" --idempotency-key "$key" "$@" --output json >"$output"
}

accept_decision() {
  output=$1
  revision=$2
  key=$3
  "$binary" knowledge accept "$revision" --workspace personal --expected-state-revision 1 \
    --note 'Accepted for contradiction acceptance.' --socket "$socket_path" \
    --idempotency-key "$key" --output json >"$output"
}

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >/dev/null

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key contradiction-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add demo --workspace personal --repo "$fixture_root/world-engine" --mode shared \
  --socket "$socket_path" --idempotency-key contradiction-project --output json >"$scenario_root/project.json"
"$binary" agent create contradiction-worker --workspace personal --role reviewer --provider fixture-mcp --runtime direct \
  --socket "$socket_path" --idempotency-key contradiction-agent --output json >"$scenario_root/agent.json"

create_task "$scenario_root/report-task.json" 'Review routing contradictions' contradiction-report-task
report_task=$(extract_id task "$scenario_root/report-task.json")
[ -n "$report_task" ] || fail 'report task ID is missing'

propose_decision "$scenario_root/left-proposed.json" "$report_task" \
  'Routing mode routing mode primary' 'Routing mode must always use the primary endpoint.' contradiction-left-proposal
left_revision=$(extract_id krev "$scenario_root/left-proposed.json")
accept_decision "$scenario_root/left-accepted.json" "$left_revision" contradiction-left-accept

propose_decision "$scenario_root/right-proposed.json" "$report_task" \
  'Routing mode routing mode replica' 'Routing mode must always use the replica endpoint.' contradiction-right-proposal \
  --task-scope "$report_task"
right_revision=$(extract_id krev "$scenario_root/right-proposed.json")
accept_decision "$scenario_root/right-accepted.json" "$right_revision" contradiction-right-accept

propose_decision "$scenario_root/safe-proposed.json" "$report_task" \
  'Routing mode fallback' 'Routing mode keeps one independent safe fallback.' contradiction-safe-proposal
safe_revision=$(extract_id krev "$scenario_root/safe-proposed.json")
accept_decision "$scenario_root/safe-accepted.json" "$safe_revision" contradiction-safe-accept
for revision in "$left_revision" "$right_revision" "$safe_revision"
do
  [ -n "$revision" ] || fail 'an accepted knowledge revision ID is missing'
done

"$binary" knowledge index rebuild --workspace personal --socket "$socket_path" \
  --idempotency-key contradiction-index --output json >"$scenario_root/index.json"
"$binary" knowledge search 'routing mode' --workspace personal --project demo --limit 100 \
  --socket "$socket_path" --output json >"$scenario_root/proposed-project-search.json"
assert_contains "$scenario_root/proposed-project-search.json" "$left_revision" 'project search omitted the project-wide participant before confirmation'
assert_contains "$scenario_root/proposed-project-search.json" "$safe_revision" 'project search omitted the safe revision before confirmation'
assert_absent "$scenario_root/proposed-project-search.json" "$right_revision" 'project search leaked the task-scoped participant'

sed \
  -e "s/__LEFT_REVISION__/$left_revision/g" \
  -e "s/__RIGHT_REVISION__/$right_revision/g" \
  "$repo_root/test/scenarios/knowledge-contradictions/report.json.in" >"$scenario_root/report-fixture.json"

"$binary" task assign "$report_task" contradiction-worker --workspace personal --lease-seconds 600 \
  --expected-revision 1 --socket "$socket_path" --idempotency-key contradiction-report-assignment \
  --output json >"$scenario_root/report-assigned.json"
"$binary" run start "$report_task" --workspace personal --runtime direct --provider fixture-mcp \
  --scenario "$scenario_root/report-fixture.json" --expected-task-revision 2 --socket "$socket_path" \
  --idempotency-key contradiction-run --output json >"$scenario_root/run.json"
report_run=$(extract_id run "$scenario_root/run.json")
[ -n "$report_run" ] || fail 'reporter run ID is missing'
"$binary" run watch "$report_run" --workspace personal --wait-seconds 15 --socket "$socket_path" \
  --output json >"$scenario_root/run-final.json"
assert_contains "$scenario_root/run-final.json" '"status":"completed"' 'fixture reporter did not complete'
"$binary" run logs "$report_run" --workspace personal --tail 100 --socket "$socket_path" \
  --output json >"$scenario_root/run-logs.json"
assert_absent "$scenario_root/run-logs.json" "$sentinel" 'raw MCP contradiction input leaked into captured provider logs'
assert_absent "$daemon_log" "$sentinel" 'raw MCP contradiction input leaked into daemon logs'

"$binary" contradiction list --workspace personal --project demo --socket "$socket_path" \
  --output json >"$scenario_root/proposed-list.json"
contradiction_id=$(extract_id kcon "$scenario_root/proposed-list.json")
[ -n "$contradiction_id" ] || fail 'proposed contradiction ID is missing'
assert_contains "$scenario_root/proposed-list.json" '"status":"proposed"' 'agent report did not remain proposed'
assert_contains "$scenario_root/proposed-list.json" "\"reported_by\":\"$report_run\",\"reported_by_type\":\"agent_run\"" 'report authority was not derived from the exact run'
assert_contains "$scenario_root/proposed-list.json" '"authority_check_count":0,"authority_checks":[]' 'reserved MCP confirmation reached contradiction governance'
assert_contains "$scenario_root/proposed-list.json" "$sentinel" 'canonical report omitted the exact MCP reason'
"$binary" contradiction show "$contradiction_id" --workspace personal --socket "$socket_path" \
  --output json >"$scenario_root/proposed-show.json"
"$binary" knowledge dispute "$left_revision" --workspace personal --socket "$socket_path" \
  --output json >"$scenario_root/proposed-dispute.json"
assert_contains "$scenario_root/proposed-dispute.json" '"disputed":false,"open_contradiction_count":0,"open_contradiction_ids":[]' 'proposed report affected derived dispute state'

create_task "$scenario_root/context-task.json" 'Consume independent routing knowledge' contradiction-context-task
context_task=$(extract_id task "$scenario_root/context-task.json")
"$binary" task assign "$context_task" contradiction-worker --workspace personal --lease-seconds 600 \
  --expected-revision 1 --socket "$socket_path" --idempotency-key contradiction-context-assignment \
  --output json >"$scenario_root/context-assigned.json"
"$binary" context build "$context_task" --workspace personal --agent contradiction-worker \
  --expected-task-revision 2 --include "$left_revision" --socket "$socket_path" \
  --idempotency-key contradiction-context-before --output json >"$scenario_root/context-before-build.json"
context_before=$(extract_id ctx "$scenario_root/context-before-build.json")
[ -n "$context_before" ] || fail 'pre-confirmation context packet ID is missing'
"$binary" context show "$context_before" --workspace personal --socket "$socket_path" \
  --output json >"$scenario_root/context-before-show.json"

"$binary" contradiction confirm "$contradiction_id" --expected-state-revision 1 \
  --note 'Owner confirmed the exact conflict.' --workspace personal --socket "$socket_path" \
  --idempotency-key contradiction-owner-confirm --output json >"$scenario_root/open.json"
assert_contains "$scenario_root/open.json" '"status":"open","state_revision":2' 'owner confirmation did not open revision 2'
assert_contains "$scenario_root/open.json" '"outcome":"allowed","reason":"workspace_owner"' 'owner confirmation authority is missing'
"$binary" contradiction show "$contradiction_id" --workspace personal --socket "$socket_path" \
  --output json >"$scenario_root/open-show.json"
assert_contains "$scenario_root/open-show.json" '"authority_check_count":1' 'open contradiction authority count is wrong'
printf '%s\n%s\n' "$left_revision" "$right_revision" | LC_ALL=C sort > "$scenario_root/canonical-pair.txt"
canonical_left=$(sed -n '1p' "$scenario_root/canonical-pair.txt")
canonical_right=$(sed -n '2p' "$scenario_root/canonical-pair.txt")
assert_contains "$scenario_root/open-show.json" "\"left_revision\":{\"id\":\"$canonical_left\"" 'open detail omitted the canonical left exact snapshot'
assert_contains "$scenario_root/open-show.json" "\"right_revision\":{\"id\":\"$canonical_right\"" 'open detail omitted the canonical right exact snapshot'

"$binary" knowledge dispute "$left_revision" --workspace personal --socket "$socket_path" \
  --output json >"$scenario_root/open-left-dispute.json"
"$binary" knowledge dispute "$right_revision" --workspace personal --socket "$socket_path" \
  --output json >"$scenario_root/open-right-dispute.json"
for dispute in "$scenario_root/open-left-dispute.json" "$scenario_root/open-right-dispute.json"
do
  assert_contains "$dispute" "\"disputed\":true,\"open_contradiction_count\":1,\"open_contradiction_ids\":[\"$contradiction_id\"]" 'open contradiction did not derive exact bounded dispute state'
done
"$binary" knowledge show "$left_revision" --workspace personal --socket "$socket_path" \
  --output json >"$scenario_root/left-open-show.json"
assert_contains "$scenario_root/left-open-show.json" '"review_status":"accepted","currency_status":"current"' 'contradiction changed canonical knowledge currency'

"$binary" knowledge search 'routing mode' --workspace personal --project demo --limit 1 \
  --socket "$socket_path" --output json >"$scenario_root/open-search.json"
assert_contains "$scenario_root/open-search.json" "$safe_revision" 'search applied LIMIT before excluding disputed participants'
assert_absent "$scenario_root/open-search.json" "$left_revision" 'project-wide disputed participant leaked into project-only search'
assert_absent "$scenario_root/open-search.json" "$right_revision" 'task-scoped disputed participant leaked into project-only search'
"$binary" knowledge search 'routing mode' --workspace personal --project demo --task "$context_task" --limit 100 \
  --socket "$socket_path" --output json >"$scenario_root/open-other-task-search.json"
assert_absent "$scenario_root/open-other-task-search.json" "$left_revision" 'project-wide participant escaped whole-revision quarantine in another task'

"$binary" events list --after 0 --limit 1000 --socket "$socket_path" \
  --output json >"$scenario_root/events-before-context-conflict.json"
if "$binary" context build "$context_task" --workspace personal --agent contradiction-worker \
  --expected-task-revision 2 --include "$left_revision" --socket "$socket_path" \
  --idempotency-key contradiction-context-retry --output json \
  >"$scenario_root/context-conflict-output.json" 2>"$scenario_root/context-conflict.json"
then
  fail 'new context build accepted an otherwise eligible disputed revision'
fi
assert_contains "$scenario_root/context-conflict.json" '"code":"knowledge_conflict"' 'context conflict returned the wrong stable code'
assert_contains "$scenario_root/context-conflict.json" "$contradiction_id" 'context conflict omitted the authorized open contradiction ID'
"$binary" events list --after 0 --limit 1000 --socket "$socket_path" \
  --output json >"$scenario_root/events-after-context-conflict.json"
cmp "$scenario_root/events-before-context-conflict.json" "$scenario_root/events-after-context-conflict.json" || fail 'failed context build appended a durable event'
"$binary" context show "$context_before" --workspace personal --socket "$socket_path" \
  --output json >"$scenario_root/context-open-show.json"
cmp "$scenario_root/context-before-show.json" "$scenario_root/context-open-show.json" || fail 'open contradiction rewrote an existing immutable packet'

"$binary" contradiction dismiss "$contradiction_id" --expected-state-revision 2 \
  --note 'Owner dismissed after resolving the false positive.' --workspace personal --socket "$socket_path" \
  --idempotency-key contradiction-owner-dismiss --output json >"$scenario_root/dismissed.json"
assert_contains "$scenario_root/dismissed.json" '"status":"dismissed","state_revision":3' 'open contradiction did not dismiss at revision 3'
"$binary" knowledge dispute "$left_revision" --workspace personal --socket "$socket_path" \
  --output json >"$scenario_root/dismissed-dispute.json"
assert_contains "$scenario_root/dismissed-dispute.json" '"disputed":false,"open_contradiction_count":0,"open_contradiction_ids":[]' 'dismissal did not clear derived dispute state'
"$binary" knowledge search 'routing mode' --workspace personal --project demo --limit 100 \
  --socket "$socket_path" --output json >"$scenario_root/dismissed-search.json"
assert_contains "$scenario_root/dismissed-search.json" "$left_revision" 'dismissal did not restore project-wide search eligibility'

# The failed open-state build wrote neither packet nor idempotency result, so the
# exact same command key succeeds once the conflict is dismissed.
"$binary" context build "$context_task" --workspace personal --agent contradiction-worker \
  --expected-task-revision 2 --include "$left_revision" --socket "$socket_path" \
  --idempotency-key contradiction-context-retry --output json >"$scenario_root/context-after-dismiss.json"
assert_contains "$scenario_root/context-after-dismiss.json" "$left_revision" 'dismissal did not restore exact context eligibility'

if "$binary" contradiction report "$right_revision" "$left_revision" \
  --reason 'Attempt to re-report a terminal exact pair.' --workspace personal --socket "$socket_path" \
  --idempotency-key contradiction-pair-rereport --output json \
  >"$scenario_root/rereport-output.json" 2>"$scenario_root/rereport-error.json"
then
  fail 'globally unique terminal revision pair was reported twice'
fi
assert_contains "$scenario_root/rereport-error.json" '"code":"contradiction_conflict"' 'terminal pair re-report returned the wrong conflict'

"$binary" contradiction list --workspace personal --project demo --socket "$socket_path" \
  --output json >"$scenario_root/active-after-dismiss.json"
assert_contains "$scenario_root/active-after-dismiss.json" '"details":[]' 'default list returned terminal contradictions'
"$binary" contradiction list --workspace personal --project demo --status dismissed --socket "$socket_path" \
  --output json >"$scenario_root/dismissed-list.json"
assert_contains "$scenario_root/dismissed-list.json" "$contradiction_id" 'dismissed filter omitted the terminal contradiction'

"$binary" events list --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events.json"
assert_count "$scenario_root/events.json" '"type":"contradiction.detected"' 1 'contradiction detection event count is wrong'
assert_count "$scenario_root/events.json" '"type":"contradiction.confirmed"' 1 'contradiction confirmation event count is wrong'
assert_count "$scenario_root/events.json" '"type":"contradiction.dismissed"' 1 'contradiction dismissal event count is wrong'
assert_count "$scenario_root/events.json" '"type":"run.tool_denied"' 1 'reserved confirmation denial count is wrong'
assert_absent "$scenario_root/events.json" '"type":"contradiction.confirm_denied"' 'reserved MCP denial improperly reached contradiction governance'

"$binary" context show "$context_before" --workspace personal --socket "$socket_path" \
  --output json >"$scenario_root/context-before-restart.json"
"$binary" contradiction show "$contradiction_id" --workspace personal --socket "$socket_path" \
  --output json >"$scenario_root/dismissed-before-restart.json"
stop_daemon first
start_daemon
"$binary" context show "$context_before" --workspace personal --socket "$socket_path" \
  --output json >"$scenario_root/context-after-restart.json"
"$binary" contradiction show "$contradiction_id" --workspace personal --socket "$socket_path" \
  --output json >"$scenario_root/dismissed-after-restart.json"
cmp "$scenario_root/context-before-restart.json" "$scenario_root/context-after-restart.json" || fail 'already-built packet changed across restart'
cmp "$scenario_root/dismissed-before-restart.json" "$scenario_root/dismissed-after-restart.json" || fail 'contradiction detail changed across restart'
"$binary" contradiction dismiss "$contradiction_id" --expected-state-revision 2 \
  --note 'Owner dismissed after resolving the false positive.' --workspace personal --socket "$socket_path" \
  --idempotency-key contradiction-owner-dismiss --output json >"$scenario_root/dismissed-replay.json"
cmp "$scenario_root/dismissed.json" "$scenario_root/dismissed-replay.json" || fail 'dismissal idempotency replay changed across restart'

stop_daemon final
printf 'Owner-confirmed knowledge contradiction acceptance: PASS\n'
