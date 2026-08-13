#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-canonical-knowledge.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
fixture_root="$scenario_root/git-fixture"
daemon_log="$scenario_root/daemon.log"
daemon_pid=''
terminal_sentinel="CREWFOLD_TERMINAL_ONLY_$$_$(date +%s)"

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'Canonical knowledge acceptance failed; collected diagnostics follow\n' >&2
    for diagnostic in \
      "$daemon_log" \
      "$scenario_root/codex-final.json" \
      "$scenario_root/codex-logs.json" \
      "$scenario_root/accepted-context.json" \
      "$scenario_root/exclusions-context.json" \
      "$scenario_root/superseded-context.json" \
      "$scenario_root/claude-final.json" \
      "$scenario_root/claude-logs.json" \
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

assert_revision_state() {
  path=$1
  revision=$2
  review=$3
  currency=$4
  message=$5
  grep -Eq "\"id\":\"$revision\"[^}]*\"review_status\":\"$review\",\"currency_status\":\"$currency\"" "$path" || fail "$message"
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

propose_finding() {
  output=$1
  source_task=$2
  markdown=$3
  key=$4
  shift 4
  "$binary" knowledge propose --type finding --from-task "$source_task" "$markdown" \
    --workspace personal --socket "$socket_path" --idempotency-key "$key" "$@" \
    --output json >"$output"
}

accept_finding() {
  output=$1
  revision=$2
  key=$3
  "$binary" knowledge accept "$revision" --expected-state-revision 1 \
    --workspace personal --socket "$socket_path" --idempotency-key "$key" \
    --note 'Owner accepted the bounded replacement-agent finding.' \
    --output json >"$output"
}

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >/dev/null

# Wrap the recorded binary only to add one unique terminal-log line after it has
# executed its normal probe/run contract. The sentinel is never a task, message,
# source, proposal, or context input.
codex_wrapper="$scenario_root/codex-with-terminal-sentinel"
sed \
  -e "s|@CODEX_FIXTURE@|$repo_root/test/fixtures/providers/codex/codex|g" \
  -e "s|@TERMINAL_SENTINEL@|$terminal_sentinel|g" \
  >"$codex_wrapper" <<'WRAPPER'
#!/bin/sh
set -eu
fixture='@CODEX_FIXTURE@'
"$fixture" "$@"
status=$?
if [ "${1:-}" = "exec" ]
then
  printf '%s\n' '@TERMINAL_SENTINEL@' >&2
fi
exit "$status"
WRAPPER
chmod 700 "$codex_wrapper"

export CREWFOLD_CODEX_BINARY="$codex_wrapper"
export CREWFOLD_CLAUDE_BINARY="$repo_root/test/fixtures/providers/claude/claude"

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key canonical-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add contacts --workspace personal --repo "$fixture_root/world-engine" --mode exclusive --socket "$socket_path" --idempotency-key canonical-project --output json >"$scenario_root/project.json"
"$binary" checkout add contacts "$fixture_root/world-engine-2" --workspace personal --mode exclusive --socket "$socket_path" --idempotency-key canonical-replacement-checkout --output json >"$scenario_root/replacement-checkout.json"
replacement_checkout=$(extract_id co "$scenario_root/replacement-checkout.json")
[ -n "$replacement_checkout" ] || fail 'replacement checkout ID is missing'

"$binary" agent create codex-worker --workspace personal --role implementer --provider codex --runtime direct --socket "$socket_path" --idempotency-key canonical-codex-agent --output json >"$scenario_root/codex-agent.json"
"$binary" agent create claude-worker --workspace personal --role implementer --provider claude --runtime direct --socket "$socket_path" --idempotency-key canonical-claude-agent --output json >"$scenario_root/claude-agent.json"

"$binary" task create --workspace personal --project contacts --title 'Produce durable ordering handoff' --description 'Complete the first step and send the replacement agent a Crewfold-owned handoff.' --socket "$socket_path" --idempotency-key canonical-producer-task --output json >"$scenario_root/producer-task.json"
producer_task=$(extract_id task "$scenario_root/producer-task.json")
[ -n "$producer_task" ] || fail 'producer task ID is missing'
"$binary" task assign "$producer_task" codex-worker --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key canonical-producer-assignment --output json >"$scenario_root/producer-assigned.json"
"$binary" run start "$producer_task" --workspace personal --runtime direct --provider codex --scenario "$repo_root/test/fixtures/providers/codex/provider-handoff.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key canonical-codex-run --output json >"$scenario_root/codex-run.json"
codex_run=$(extract_id run "$scenario_root/codex-run.json")
[ -n "$codex_run" ] || fail 'Codex run ID is missing'
"$binary" run watch "$codex_run" --workspace personal --wait-seconds 15 --socket "$socket_path" --output json >"$scenario_root/codex-final.json"
assert_contains "$scenario_root/codex-final.json" '"status":"completed"' 'Codex run did not complete'
assert_contains "$scenario_root/codex-final.json" 'The continuation is stored in Claude worker durable mail' 'Codex did not produce the accepted durable handoff'
"$binary" run logs "$codex_run" --workspace personal --tail 100 --socket "$socket_path" --output json >"$scenario_root/codex-logs.json"
grep -Fq "$terminal_sentinel" "$scenario_root/codex-logs.json" || fail 'unique terminal sentinel is absent from Codex runtime logs'

"$binary" task create --workspace personal --project contacts --title 'Continue from canonical ordering knowledge' --description 'Use the durable provider handoff and the exact accepted contact ordering revision. Required evidence: provider_handoff_received and canonical_knowledge_received.' --socket "$socket_path" --idempotency-key canonical-replacement-task --output json >"$scenario_root/replacement-task.json"
replacement_task=$(extract_id task "$scenario_root/replacement-task.json")
"$binary" task depend "$replacement_task" --on "$producer_task" --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key canonical-dependency --output json >"$scenario_root/replacement-dependent.json"
"$binary" task assign "$replacement_task" claude-worker --lease-seconds 600 --workspace personal --expected-revision 2 --socket "$socket_path" --idempotency-key canonical-replacement-assignment --output json >"$scenario_root/replacement-assigned.json"

"$binary" task create --workspace personal --project contacts --title 'Other contact task scope' --socket "$socket_path" --idempotency-key canonical-other-task --output json >"$scenario_root/other-task.json"
other_task=$(extract_id task "$scenario_root/other-task.json")

accepted_fixture="$repo_root/test/fixtures/knowledge/accepted-finding.md"
successor_fixture="$repo_root/test/fixtures/knowledge/successor-finding.md"
propose_finding "$scenario_root/accepted-proposed.json" "$producer_task" "$accepted_fixture" canonical-accepted-proposal
accepted_revision=$(extract_id krev "$scenario_root/accepted-proposed.json")
accept_finding "$scenario_root/accepted.json" "$accepted_revision" canonical-accepted-decision
assert_revision_state "$scenario_root/accepted.json" "$accepted_revision" accepted current 'owner acceptance did not produce current canonical knowledge'
assert_contains "$scenario_root/accepted.json" '"state_revision":2' 'accepted knowledge state revision is not 2'
assert_contains "$scenario_root/accepted.json" '"outcome":"allowed"' 'owner acceptance authority was not recorded as allowed'
"$binary" knowledge show "$accepted_revision" --workspace personal --socket "$socket_path" --output json >"$scenario_root/accepted-show.json"
assert_contains "$scenario_root/accepted-show.json" '"title":"Accepted contact ordering contract"' 'knowledge show omitted the accepted revision title'
assert_contains "$scenario_root/accepted-show.json" '"authority_checks":[{' 'knowledge show omitted inspectable authority history'
assert_contains "$scenario_root/accepted-show.json" '"action":"accept"' 'knowledge show omitted acceptance authority'

propose_finding "$scenario_root/proposed.json" "$producer_task" "$accepted_fixture" canonical-unaccepted-proposal
proposed_revision=$(extract_id krev "$scenario_root/proposed.json")

propose_finding "$scenario_root/stale-proposed.json" "$producer_task" "$accepted_fixture" canonical-stale-proposal
stale_revision=$(extract_id krev "$scenario_root/stale-proposed.json")
accept_finding "$scenario_root/stale-accepted.json" "$stale_revision" canonical-stale-accept
"$binary" knowledge mark-stale "$stale_revision" --expected-state-revision 2 --reason 'The bounded acceptance fixture deliberately invalidates this revision.' --workspace personal --socket "$socket_path" --idempotency-key canonical-mark-stale --output json >"$scenario_root/stale.json"
assert_revision_state "$scenario_root/stale.json" "$stale_revision" accepted stale 'stale transition did not preserve accepted review state'
assert_contains "$scenario_root/stale.json" '"state_revision":3' 'stale knowledge state revision is not 3'
assert_contains "$scenario_root/stale.json" '"action":"mark_stale"' 'stale authority record is missing'

propose_finding "$scenario_root/out-of-scope-proposed.json" "$other_task" "$accepted_fixture" canonical-out-of-scope-proposal --task-scope "$other_task"
out_of_scope_revision=$(extract_id krev "$scenario_root/out-of-scope-proposed.json")
accept_finding "$scenario_root/out-of-scope-accepted.json" "$out_of_scope_revision" canonical-out-of-scope-accept

# Generate the oversized body inside the disposable scenario rather than keeping
# a large committed fixture. It fits the 16 KiB knowledge record but exceeds the
# 12 KiB knowledge section budget as one indivisible revision.
oversized_markdown="$scenario_root/oversized-finding.md"
{
  printf '# Oversized accepted context fixture\n\n'
  awk 'BEGIN { for (i = 0; i < 13500; i++) printf "x"; printf "\n" }'
} >"$oversized_markdown"
propose_finding "$scenario_root/oversized-proposed.json" "$producer_task" "$oversized_markdown" canonical-oversized-proposal
oversized_revision=$(extract_id krev "$scenario_root/oversized-proposed.json")
accept_finding "$scenario_root/oversized-accepted.json" "$oversized_revision" canonical-oversized-accept

# The first explicit packet demonstrates repeated --include ordering plus every
# pre-supersession eligibility outcome required by the canonical-knowledge gate.
"$binary" context build "$replacement_task" --workspace personal --agent claude-worker --expected-task-revision 3 \
  --checkout "$replacement_checkout" \
  --include "$accepted_revision" \
  --include "$proposed_revision" \
  --include "$stale_revision" \
  --include "$out_of_scope_revision" \
  --include "$oversized_revision" \
  --socket "$socket_path" --idempotency-key canonical-exclusion-context --output json >"$scenario_root/exclusions-context.json"
exclusions_context=$(extract_id ctx "$scenario_root/exclusions-context.json")
assert_contains "$scenario_root/exclusions-context.json" "\"requested_knowledge_revision_ids\":[\"$accepted_revision\",\"$proposed_revision\",\"$stale_revision\",\"$out_of_scope_revision\",\"$oversized_revision\"]" 'context packet did not preserve repeated --include order'
assert_contains "$scenario_root/exclusions-context.json" "\"accepted_knowledge\":[{\"id\":\"$accepted_revision\"" 'context packet omitted the one eligible accepted revision'
assert_contains "$scenario_root/exclusions-context.json" "\"requested_revision_id\":\"$proposed_revision\",\"reason_code\":\"proposed\"" 'proposed exclusion reason is missing'
assert_contains "$scenario_root/exclusions-context.json" "\"requested_revision_id\":\"$stale_revision\",\"reason_code\":\"stale\"" 'stale exclusion reason is missing'
assert_contains "$scenario_root/exclusions-context.json" "\"requested_revision_id\":\"$out_of_scope_revision\",\"reason_code\":\"out_of_scope\"" 'out-of-scope exclusion reason is missing'
assert_contains "$scenario_root/exclusions-context.json" "\"requested_revision_id\":\"$oversized_revision\",\"reason_code\":\"over_budget\"" 'over-budget exclusion reason is missing'
knowledge_budget=$(sed -n 's/.*"knowledge":{"limit_bytes":12288,"used_bytes":\([0-9][0-9]*\),"remaining_bytes":[0-9][0-9]*}.*/\1/p' "$scenario_root/exclusions-context.json")
[ -n "$knowledge_budget" ] && [ "$knowledge_budget" -gt 0 ] && [ "$knowledge_budget" -le 12288 ] || fail 'accepted knowledge budget accounting is invalid'
"$binary" context explain "$exclusions_context" --workspace personal --socket "$socket_path" --output json >"$scenario_root/exclusions-explain.json"
assert_contains "$scenario_root/exclusions-explain.json" "\"packet_id\":\"$exclusions_context\"" 'context explanation identifies the wrong packet'
assert_contains "$scenario_root/exclusions-explain.json" "\"entity_id\":\"$accepted_revision\",\"revision\":1,\"reason\":\"the exact accepted current knowledge revision was explicitly requested\"" 'context explain omitted accepted selection evidence'
assert_contains "$scenario_root/exclusions-explain.json" "\"requested_revision_id\":\"$oversized_revision\",\"reason_code\":\"over_budget\"" 'context explain omitted over-budget evidence'

# A dedicated packet binds only the accepted revision to the replacement run.
"$binary" context build "$replacement_task" --workspace personal --agent claude-worker --expected-task-revision 3 \
  --checkout "$replacement_checkout" --include "$accepted_revision" --socket "$socket_path" --idempotency-key canonical-accepted-context --output json >"$scenario_root/accepted-context.json"
accepted_context=$(extract_id ctx "$scenario_root/accepted-context.json")
"$binary" context show "$accepted_context" --workspace personal --socket "$socket_path" --output json >"$scenario_root/accepted-show-before-restart.json"
"$binary" context explain "$accepted_context" --workspace personal --socket "$socket_path" --output json >"$scenario_root/accepted-explain-before-restart.json"
assert_contains "$scenario_root/accepted-show-before-restart.json" "\"accepted_knowledge\":[{\"id\":\"$accepted_revision\"" 'dedicated replacement packet omitted accepted canonical knowledge'
assert_contains "$scenario_root/accepted-show-before-restart.json" '"title":"Accepted contact ordering contract"' 'dedicated replacement packet omitted the accepted title'
if grep -Fq "$terminal_sentinel" "$scenario_root/accepted-show-before-restart.json" "$scenario_root/accepted-explain-before-restart.json"
then
  fail 'terminal-log sentinel leaked into a canonical context response'
fi

stop_daemon first
start_daemon
"$binary" context show "$accepted_context" --workspace personal --socket "$socket_path" --output json >"$scenario_root/accepted-show-after-restart.json"
"$binary" context explain "$accepted_context" --workspace personal --socket "$socket_path" --output json >"$scenario_root/accepted-explain-after-restart.json"
cmp "$scenario_root/accepted-show-before-restart.json" "$scenario_root/accepted-show-after-restart.json"
cmp "$scenario_root/accepted-explain-before-restart.json" "$scenario_root/accepted-explain-after-restart.json"

"$binary" run start "$replacement_task" --workspace personal --checkout "$replacement_checkout" --context "$accepted_context" --runtime direct --provider claude --scenario "$repo_root/test/fixtures/knowledge/claude-replacement.json" --expected-task-revision 3 --socket "$socket_path" --idempotency-key canonical-claude-run --output json >"$scenario_root/claude-run.json"
claude_run=$(extract_id run "$scenario_root/claude-run.json")
"$binary" run watch "$claude_run" --workspace personal --wait-seconds 15 --socket "$socket_path" --output json >"$scenario_root/claude-final.json"
assert_contains "$scenario_root/claude-final.json" '"status":"completed"' 'Claude replacement run did not complete'
assert_contains "$scenario_root/claude-final.json" '"evidence":["implementation_complete","tests_passed","provider_handoff_received","canonical_knowledge_received"]' 'Claude did not complete with handoff and canonical-knowledge evidence'

# Accepting a successor atomically supersedes the old revision. Already-built packet
# bytes remain unchanged; a new exact-pin request excludes old and includes new.
propose_finding "$scenario_root/successor-proposed.json" "$producer_task" "$successor_fixture" canonical-successor-proposal --supersedes "$accepted_revision"
successor_revision=$(extract_id krev "$scenario_root/successor-proposed.json")
accept_finding "$scenario_root/successor-accepted.json" "$successor_revision" canonical-successor-accept
assert_revision_state "$scenario_root/successor-accepted.json" "$successor_revision" accepted current 'successor was not accepted as current knowledge'
assert_contains "$scenario_root/successor-accepted.json" '"revision_number":2' 'successor is not revision 2'
assert_contains "$scenario_root/successor-accepted.json" "\"supersedes_revision_id\":\"$accepted_revision\"" 'successor does not name its predecessor'
"$binary" knowledge show "$accepted_revision" --workspace personal --socket "$socket_path" --output json >"$scenario_root/predecessor-after-successor.json"
"$binary" knowledge show "$successor_revision" --workspace personal --socket "$socket_path" --output json >"$scenario_root/successor-show.json"
assert_revision_state "$scenario_root/predecessor-after-successor.json" "$accepted_revision" accepted superseded 'predecessor history was not preserved as superseded'
assert_contains "$scenario_root/predecessor-after-successor.json" '"state_revision":3' 'superseded predecessor state revision is not 3'
assert_contains "$scenario_root/predecessor-after-successor.json" '"action":"supersede"' 'predecessor supersession authority is missing'
assert_revision_state "$scenario_root/successor-show.json" "$successor_revision" accepted current 'current successor is missing'
assert_contains "$scenario_root/successor-show.json" '"action":"accept"' 'successor acceptance authority is missing'
"$binary" knowledge list --workspace personal --project contacts --review-status accepted --socket "$socket_path" --output json >"$scenario_root/accepted-list.json"
assert_revision_state "$scenario_root/accepted-list.json" "$accepted_revision" accepted superseded 'knowledge list omitted superseded history'
assert_revision_state "$scenario_root/accepted-list.json" "$successor_revision" accepted current 'knowledge list omitted the current successor'

"$binary" context show "$accepted_context" --workspace personal --socket "$socket_path" --output json >"$scenario_root/accepted-show-after-supersession.json"
"$binary" context explain "$accepted_context" --workspace personal --socket "$socket_path" --output json >"$scenario_root/accepted-explain-after-supersession.json"
cmp "$scenario_root/accepted-show-before-restart.json" "$scenario_root/accepted-show-after-supersession.json"
cmp "$scenario_root/accepted-explain-before-restart.json" "$scenario_root/accepted-explain-after-supersession.json"

# A fresh packet names both revisions. Exact-pin semantics exclude the old ID,
# expose its replacement, and include the current successor only because it was
# itself explicitly requested.
"$binary" task create --workspace personal --project contacts --title 'Audit exact successor selection' --socket "$socket_path" --idempotency-key canonical-audit-task --output json >"$scenario_root/audit-task.json"
audit_task=$(extract_id task "$scenario_root/audit-task.json")
"$binary" task assign "$audit_task" claude-worker --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key canonical-audit-assignment --output json >"$scenario_root/audit-assigned.json"
"$binary" context build "$audit_task" --workspace personal --agent claude-worker --expected-task-revision 2 \
  --include "$accepted_revision" --include "$successor_revision" --socket "$socket_path" --idempotency-key canonical-superseded-context --output json >"$scenario_root/superseded-context.json"
superseded_context=$(extract_id ctx "$scenario_root/superseded-context.json")
assert_contains "$scenario_root/superseded-context.json" "\"requested_knowledge_revision_ids\":[\"$accepted_revision\",\"$successor_revision\"]" 'new packet did not preserve exact requested revision pins'
assert_contains "$scenario_root/superseded-context.json" "\"accepted_knowledge\":[{\"id\":\"$successor_revision\"" 'new packet did not explicitly select the current successor'
assert_contains "$scenario_root/superseded-context.json" "\"requested_revision_id\":\"$accepted_revision\",\"replacement_revision_id\":\"$successor_revision\",\"reason_code\":\"superseded\"" 'new packet silently followed or failed to explain the superseded pin'
"$binary" context explain "$superseded_context" --workspace personal --socket "$socket_path" --output json >"$scenario_root/superseded-explain.json"
assert_contains "$scenario_root/superseded-explain.json" "\"entity_id\":\"$successor_revision\",\"revision\":2,\"reason\":\"the exact accepted current knowledge revision was explicitly requested\"" 'context explanation omitted explicit successor inclusion'
assert_contains "$scenario_root/superseded-explain.json" "\"requested_revision_id\":\"$accepted_revision\",\"replacement_revision_id\":\"$successor_revision\",\"reason_code\":\"superseded\"" 'context explanation omitted exact-pin successor metadata'

"$binary" run logs "$claude_run" --workspace personal --tail 100 --socket "$socket_path" --output json >"$scenario_root/claude-logs.json"
if grep -Fq "$terminal_sentinel" "$scenario_root/claude-logs.json"
then
  fail 'Codex terminal sentinel crossed the provider boundary'
fi

"$binary" events list --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events.json"
for event_type in knowledge.proposed knowledge.accepted knowledge.marked_stale knowledge.superseded context.packet_built message.sent run.report_received
do
  grep -Fq "\"type\":\"$event_type\"" "$scenario_root/events.json" || fail "missing durable event $event_type"
done

stop_daemon final

# The public context responses and the complete canonical SQLite database must not
# contain terminal-only output after a clean stop. Runtime logs intentionally do.
if grep -Fq "$terminal_sentinel" \
  "$scenario_root/accepted-show-after-supersession.json" \
  "$scenario_root/accepted-explain-after-supersession.json" \
  "$scenario_root/superseded-context.json" \
  "$scenario_root/superseded-explain.json"
then
  fail 'terminal sentinel leaked into immutable context data'
fi
if grep -aFq "$terminal_sentinel" "$data_dir/crewfold.db" "$data_dir/crewfold.db-wal" "$data_dir/crewfold.db-shm" 2>/dev/null
then
  fail 'terminal sentinel leaked into canonical SQLite storage'
fi

printf 'Canonical knowledge and provider-switch acceptance: PASS\n'
