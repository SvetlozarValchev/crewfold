#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-live-context-deltas.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
repos_root="$scenario_root/repos"
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
    printf 'live context delta acceptance failed; collected diagnostics follow\n' >&2
    for diagnostic in "$daemon_log" "$scenario_root/main-packet.json" "$scenario_root/refresh-created.json" "$scenario_root/refresh-pending.json" "$scenario_root/refresh-two.json" "$scenario_root/refresh-three.json" "$scenario_root/delta-before-restart.json" "$scenario_root/delta-after-restart.json" "$scenario_root/deltas-final.json" "$scenario_root/wrong-refresh.json" "$scenario_root/noop-refresh.json" "$scenario_root/events-final.json" "$scenario_root/main-show.json" "$scenario_root/wrong-show.json"
    do
      if [ -f "$diagnostic" ]
      then
        printf '%s\n' "$diagnostic" >&2
        sed -n '1,320p' "$diagnostic" >&2
      fi
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
      sed -n '1,320p' "$daemon_log" >&2
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

init_repository() {
  repository_path=$1
  label=$2
  git init --quiet --initial-branch=main "$repository_path"
  git -C "$repository_path" config user.name 'Crewfold Fixture'
  git -C "$repository_path" config user.email 'fixture@crewfold.invalid'
  printf '%s\n' "$label" >"$repository_path/README.md"
  git -C "$repository_path" add README.md
  GIT_AUTHOR_DATE='2026-01-01T00:00:00Z' GIT_COMMITTER_DATE='2026-01-01T00:00:00Z' \
    git -C "$repository_path" commit --quiet -m "initialize $label"
}

wait_run_active() {
  run_id=$1
  output=$2
  attempts=0
  while :
  do
    "$binary" run show "$run_id" --workspace personal --socket "$socket_path" --output json >"$output"
    if grep -Fq '"status":"active"' "$output"
    then
      return
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 400 ]
    then
      printf 'run %s never became active\n' "$run_id" >&2
      exit 1
    fi
    sleep 0.025
  done
}

wait_acknowledged() {
  run_id=$1
  sequence=$2
  output=$3
  attempts=0
  while :
  do
    "$binary" context delta list "$run_id" --workspace personal --after-sequence 0 --limit 100 --socket "$socket_path" --output json >"$output"
    if grep -Fq "\"last_acknowledged_sequence\":$sequence" "$output"
    then
      return
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 800 ]
    then
      printf 'run %s did not acknowledge context delta sequence %s\n' "$run_id" "$sequence" >&2
      exit 1
    fi
    sleep 0.025
  done
}

wait_run_message() {
  run_id=$1
  message=$2
  output=$3
  attempts=0
  while :
  do
    "$binary" run show "$run_id" --workspace personal --socket "$socket_path" --output json >"$output"
    if grep -Fq "$message" "$output"
    then
      return
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 800 ]
    then
      printf 'run %s never recorded fixture message %s\n' "$run_id" "$message" >&2
      exit 1
    fi
    sleep 0.025
  done
}

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
mkdir -p "$repos_root"
init_repository "$repos_root/main" live-context-main
init_repository "$repos_root/library" live-context-library

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key live-context-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add main --workspace personal --repo "$repos_root/main" --mode shared --socket "$socket_path" --idempotency-key live-context-main-project --output json >"$scenario_root/main-project.json"
"$binary" project add library --workspace personal --repo "$repos_root/library" --mode shared --socket "$socket_path" --idempotency-key live-context-library-project --output json >"$scenario_root/library-project.json"

"$binary" agent create main-agent --workspace personal --role implementer --provider fixture-mcp --runtime direct --max-concurrency 2 --socket "$socket_path" --idempotency-key live-context-main-agent --output json >"$scenario_root/main-agent.json"
"$binary" agent create library-agent --workspace personal --role maintainer --provider fixture-mcp --runtime direct --socket "$socket_path" --idempotency-key live-context-library-agent --output json >"$scenario_root/library-agent.json"

"$binary" task create --workspace personal --project main --title 'Main live task' --socket "$socket_path" --idempotency-key live-context-main-task --output json >"$scenario_root/main-task.json"
"$binary" task create --workspace personal --project main --title 'Same agent wrong task' --socket "$socket_path" --idempotency-key live-context-wrong-task --output json >"$scenario_root/wrong-task.json"
"$binary" task create --workspace personal --project library --title 'Library participant task' --socket "$socket_path" --idempotency-key live-context-library-task --output json >"$scenario_root/library-task.json"
main_task=$(extract_id task "$scenario_root/main-task.json")
wrong_task=$(extract_id task "$scenario_root/wrong-task.json")
library_task=$(extract_id task "$scenario_root/library-task.json")

"$binary" task assign "$main_task" main-agent --lease-seconds 900 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key live-context-main-assign --output json >"$scenario_root/main-assigned.json"
"$binary" task assign "$wrong_task" main-agent --lease-seconds 900 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key live-context-wrong-assign --output json >"$scenario_root/wrong-assigned.json"
"$binary" task assign "$library_task" library-agent --lease-seconds 900 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key live-context-library-assign --output json >"$scenario_root/library-assigned.json"

for label in baseline-a baseline-b new-decision
do
  "$binary" knowledge propose "$repo_root/test/fixtures/live-context-deltas/$label.md" --workspace personal --project main --task-scope "$main_task" --type decision --from-task "$main_task" --socket "$socket_path" --idempotency-key "live-context-$label" --output json >"$scenario_root/$label.json"
done
revision_a=$(extract_id krev "$scenario_root/baseline-a.json")
revision_b=$(extract_id krev "$scenario_root/baseline-b.json")
new_revision=$(extract_id krev "$scenario_root/new-decision.json")
"$binary" knowledge accept "$revision_a" --workspace personal --expected-state-revision 1 --socket "$socket_path" --idempotency-key live-context-accept-a --output json >"$scenario_root/accepted-a.json"
"$binary" knowledge accept "$revision_b" --workspace personal --expected-state-revision 1 --socket "$socket_path" --idempotency-key live-context-accept-b --output json >"$scenario_root/accepted-b.json"
"$binary" contradiction report "$revision_a" "$revision_b" --reason 'The two baseline orderings cannot both govern the exact task.' --workspace personal --socket "$socket_path" --idempotency-key live-context-contradiction --output json >"$scenario_root/contradiction.json"
contradiction=$(extract_id kcon "$scenario_root/contradiction.json")

"$binary" task create --workspace personal --project main --title 'Base reverse dependent' --socket "$socket_path" --idempotency-key live-context-base-dependent --output json >"$scenario_root/base-dependent.json"
base_dependent=$(extract_id task "$scenario_root/base-dependent.json")
"$binary" task depend "$base_dependent" --on "$main_task" --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key live-context-base-dependency --output json >"$scenario_root/base-dependency.json"
"$binary" thread create --workspace personal --subject 'Base participant roster' --participant "main-agent=$main_task" --participant "library-agent=$library_task" --socket "$socket_path" --idempotency-key live-context-base-thread --output json >"$scenario_root/base-thread.json"
base_thread=$(extract_id thread "$scenario_root/base-thread.json")

"$binary" context build "$main_task" --workspace personal --agent main-agent --include "$revision_a" --include "$revision_b" --expected-task-revision 2 --socket "$socket_path" --idempotency-key live-context-main-packet --output json >"$scenario_root/main-packet.json"
main_context=$(extract_id ctx "$scenario_root/main-packet.json")
grep -Fq 'urn:crewfold:schema:domain:context-packet:v1' "$scenario_root/main-packet.json"
grep -Eq '"as_of_event_sequence":[1-9][0-9]*' "$scenario_root/main-packet.json"
grep -Fq '"delivery":"explicit_pull"' "$scenario_root/main-packet.json"
grep -Fq '"ack_authority":"bound_run"' "$scenario_root/main-packet.json"
grep -Fq '"max_pending":1' "$scenario_root/main-packet.json"
grep -Fq '"max_relevant_events":1000' "$scenario_root/main-packet.json"
grep -Fq '"per_delta_limit_bytes":16384' "$scenario_root/main-packet.json"
grep -Fq '"cumulative_delta_limit_bytes":65536' "$scenario_root/main-packet.json"
grep -Fq "\"dependent_task_count\":1" "$scenario_root/main-packet.json"
grep -Fq "$base_dependent" "$scenario_root/main-packet.json"
grep -Fq "$base_thread" "$scenario_root/main-packet.json"
grep -Fq 'crewfold_get_context_delta' "$scenario_root/main-packet.json"
grep -Fq 'crewfold_acknowledge_context_delta' "$scenario_root/main-packet.json"

# These changes occur strictly after the immutable base cursor.
"$binary" thread create --workspace personal --subject 'Delta participant roster' --participant "main-agent=$main_task" --participant "library-agent=$library_task" --socket "$socket_path" --idempotency-key live-context-delta-thread --output json >"$scenario_root/delta-thread.json"
delta_thread=$(extract_id thread "$scenario_root/delta-thread.json")
"$binary" task create --workspace personal --project main --title 'Live reverse dependent' --socket "$socket_path" --idempotency-key live-context-live-dependent --output json >"$scenario_root/live-dependent.json"
live_dependent=$(extract_id task "$scenario_root/live-dependent.json")
"$binary" task depend "$live_dependent" --on "$main_task" --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key live-context-live-dependency --output json >"$scenario_root/live-dependency.json"
message_body='live-context bounded preview xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx message-tail-must-not-enter-delta'
message_preview=$(printf '%s' "$message_body" | cut -c 1-160)
message_preview="${message_preview}…"
"$binary" message send main-agent --workspace personal --thread "$delta_thread" --kind inform --body "$message_body" --socket "$socket_path" --idempotency-key live-context-message --output json >"$scenario_root/message.json"

sed -e "s/@DELTA_THREAD@/$delta_thread/g" -e "s/@NEW_REVISION@/$new_revision/g" \
  -e "s/@REVISION_A@/$revision_a/g" -e "s/@REVISION_B@/$revision_b/g" \
  -e "s/@MESSAGE_PREVIEW@/$message_preview/g" \
  -e "s/@LIVE_DEPENDENT@/$live_dependent/g" -e "s/@CONTRADICTION@/$contradiction/g" \
  "$repo_root/test/fixtures/live-context-deltas/main.json.in" >"$scenario_root/main-scenario.json"
"$binary" run start "$main_task" --workspace personal --context "$main_context" --runtime direct --provider fixture-mcp --scenario "$scenario_root/main-scenario.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key live-context-main-run --output json >"$scenario_root/main-run.json"
main_run=$(extract_id run "$scenario_root/main-run.json")
wait_run_active "$main_run" "$scenario_root/main-show.json"

# Acceptance alone does not construct a delta. The explicit refresh does.
"$binary" knowledge accept "$new_revision" --workspace personal --expected-state-revision 1 --socket "$socket_path" --idempotency-key live-context-accept-new --output json >"$scenario_root/accepted-new.json"
"$binary" context delta list "$main_run" --workspace personal --after-sequence 0 --limit 100 --socket "$socket_path" --output json >"$scenario_root/before-refresh.json"
grep -Fq '"delta_count":0' "$scenario_root/before-refresh.json"
"$binary" context refresh "$main_run" --workspace personal --socket "$socket_path" --idempotency-key live-context-refresh-one --output json >"$scenario_root/refresh-created.json"
grep -Fq '"status":"created"' "$scenario_root/refresh-created.json"
created_event_one=$(sed -n 's/.*"event_sequence":\([0-9][0-9]*\).*/\1/p' "$scenario_root/refresh-created.json" | sed -n '1p')
if [ -z "$created_event_one" ] || [ "$created_event_one" -le 0 ]
then
  printf 'created refresh omitted its positive build event sequence\n' >&2
  exit 1
fi
grep -Fq '"sequence":1' "$scenario_root/refresh-created.json"
grep -Fq '"kind":"knowledge_accepted"' "$scenario_root/refresh-created.json"
grep -Fq '"kind":"message_received"' "$scenario_root/refresh-created.json"
grep -Fq '"kind":"dependent_added"' "$scenario_root/refresh-created.json"
grep -Fq '"kind":"participant_roster_updated"' "$scenario_root/refresh-created.json"
if grep -Fq 'The exact main task must use the newly accepted live-context contract.' "$scenario_root/refresh-created.json" && grep -Fq 'live-context bounded preview' "$scenario_root/refresh-created.json"
then
  :
else
  printf 'first live delta omitted its complete decision or bounded preview\n' >&2
  exit 1
fi
if grep -Fq 'message-tail-must-not-enter-delta' "$scenario_root/refresh-created.json"
then
  printf 'first live delta exposed a full message body instead of its bounded preview\n' >&2
  exit 1
fi
delta_one=$(extract_id cdelta "$scenario_root/refresh-created.json")
"$binary" context refresh "$main_run" --workspace personal --socket "$socket_path" --idempotency-key live-context-refresh-pending --output json >"$scenario_root/refresh-pending.json"
grep -Fq '"status":"pending"' "$scenario_root/refresh-pending.json"
grep -Fq '"event_sequence":0' "$scenario_root/refresh-pending.json"
grep -Fq "$delta_one" "$scenario_root/refresh-pending.json"
"$binary" context delta show "$delta_one" --workspace personal --socket "$socket_path" --output json >"$scenario_root/delta-before-restart.json"
"$binary" context delta explain "$delta_one" --workspace personal --socket "$socket_path" --output json >"$scenario_root/delta-explain.json"
grep -Fq '"type":"context_delta_explanation"' "$scenario_root/delta-explain.json"

# The daemon restarts while the owner-built delta is still pending. The fixture's
# bounded initial delay ensures it cannot acknowledge before this barrier.
stop_daemon
start_daemon
"$binary" context delta show "$delta_one" --workspace personal --socket "$socket_path" --output json >"$scenario_root/delta-after-restart.json"
cmp "$scenario_root/delta-before-restart.json" "$scenario_root/delta-after-restart.json"

sed -e "s/@OTHER_DELTA@/$delta_one/g" "$repo_root/test/fixtures/live-context-deltas/wrong-task.json.in" >"$scenario_root/wrong-scenario.json"
"$binary" run start "$wrong_task" --workspace personal --runtime direct --provider fixture-mcp --scenario "$scenario_root/wrong-scenario.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key live-context-wrong-run --output json >"$scenario_root/wrong-run.json"
wrong_run=$(extract_id run "$scenario_root/wrong-run.json")
wait_run_active "$wrong_run" "$scenario_root/wrong-show.json"
"$binary" context refresh "$wrong_run" --workspace personal --socket "$socket_path" --idempotency-key live-context-wrong-refresh --output json >"$scenario_root/wrong-refresh.json"
grep -Fq '"status":"up_to_date"' "$scenario_root/wrong-refresh.json"
grep -Fq '"event_sequence":0' "$scenario_root/wrong-refresh.json"
if grep -Fq "$delta_thread" "$scenario_root/wrong-refresh.json" || grep -Fq "$new_revision" "$scenario_root/wrong-refresh.json"
then
  printf 'wrong-task refresh leaked participant or task-scoped knowledge\n' >&2
  exit 1
fi

wait_acknowledged "$main_run" 1 "$scenario_root/acked-one.json"
"$binary" contradiction confirm "$contradiction" --workspace personal --expected-state-revision 1 --socket "$socket_path" --idempotency-key live-context-confirm --output json >"$scenario_root/confirmed.json"
"$binary" context refresh "$main_run" --workspace personal --socket "$socket_path" --idempotency-key live-context-refresh-two --output json >"$scenario_root/refresh-two.json"
grep -Fq '"status":"created"' "$scenario_root/refresh-two.json"
created_event_two=$(sed -n 's/.*"event_sequence":\([0-9][0-9]*\).*/\1/p' "$scenario_root/refresh-two.json" | sed -n '1p')
if [ -z "$created_event_two" ] || [ "$created_event_two" -le 0 ]
then
  printf 'second created refresh omitted its positive build event sequence\n' >&2
  exit 1
fi
grep -Fq '"kind":"contradiction_opened"' "$scenario_root/refresh-two.json"
withdrawn_count=$(grep -o '"kind":"knowledge_withdrawn"' "$scenario_root/refresh-two.json" | wc -l)
if [ "$withdrawn_count" -ne 2 ]
then
  printf 'dispute delta withdrew %s decisions, want exactly 2\n' "$withdrawn_count" >&2
  exit 1
fi
grep -Fq "$contradiction" "$scenario_root/refresh-two.json"
grep -Fq "\"revision_id\":\"$revision_a\"" "$scenario_root/refresh-two.json"
grep -Fq "\"revision_id\":\"$revision_b\"" "$scenario_root/refresh-two.json"
wait_acknowledged "$main_run" 2 "$scenario_root/acked-two.json"

"$binary" contradiction dismiss "$contradiction" --workspace personal --expected-state-revision 2 --socket "$socket_path" --idempotency-key live-context-dismiss --output json >"$scenario_root/dismissed.json"
"$binary" context refresh "$main_run" --workspace personal --socket "$socket_path" --idempotency-key live-context-refresh-three --output json >"$scenario_root/refresh-three.json"
grep -Fq '"status":"created"' "$scenario_root/refresh-three.json"
created_event_three=$(sed -n 's/.*"event_sequence":\([0-9][0-9]*\).*/\1/p' "$scenario_root/refresh-three.json" | sed -n '1p')
if [ -z "$created_event_three" ] || [ "$created_event_three" -le 0 ]
then
  printf 'third created refresh omitted its positive build event sequence\n' >&2
  exit 1
fi
grep -Fq '"kind":"contradiction_closed"' "$scenario_root/refresh-three.json"
reoffered_count=$(grep -o '"kind":"knowledge_accepted"' "$scenario_root/refresh-three.json" | wc -l)
if [ "$reoffered_count" -ne 2 ]
then
  printf 'closure delta reoffered %s decisions, want exactly 2\n' "$reoffered_count" >&2
  exit 1
fi
grep -Fq '"reason":"contradiction_closed_reoffer"' "$scenario_root/refresh-three.json"
grep -Fq "\"id\":\"$revision_a\"" "$scenario_root/refresh-three.json"
grep -Fq "\"id\":\"$revision_b\"" "$scenario_root/refresh-three.json"
wait_acknowledged "$main_run" 3 "$scenario_root/acked-three.json"

# Wait until both fixtures have finished their assertions and reached their
# deliberate hang, then prove an event-free no-op cursor advance.
wait_run_message "$main_run" 'live context sequence consumed' "$scenario_root/main-show.json"
wait_run_message "$wrong_run" 'wrong task remained isolated' "$scenario_root/wrong-show.json"
"$binary" context refresh "$main_run" --workspace personal --socket "$socket_path" --idempotency-key live-context-refresh-one --output json >"$scenario_root/refresh-created-replay.json"
cmp "$scenario_root/refresh-created.json" "$scenario_root/refresh-created-replay.json"
"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events-before-noop.json"
scan_before_noop=$(sed -n 's/.*"scanned_through_event_sequence":\([0-9][0-9]*\).*/\1/p' "$scenario_root/acked-three.json" | sed -n '1p')
if [ -z "$scan_before_noop" ]
then
  printf 'acknowledged delta chain omitted its inspected cursor\n' >&2
  exit 1
fi
"$binary" events list --workspace personal --after "$scan_before_noop" --limit 1000 --socket "$socket_path" --output json >"$scenario_root/noop-gap-events.json"
grep -Fq '"type":"run.progress_reported"' "$scenario_root/noop-gap-events.json"
if grep -Eq '"type":"(message\.sent|knowledge\.(accepted|marked_stale|superseded|imported)|contradiction\.(confirmed|dismissed|resolved|imported)|thread\.(created|participant_added))"' "$scenario_root/noop-gap-events.json" ||
  grep -Fq '"entity":{"type":"task"' "$scenario_root/noop-gap-events.json" ||
  grep -Fq '"entity":{"type":"agent"' "$scenario_root/noop-gap-events.json"
then
  printf 'no-op cursor window unexpectedly contains a potentially applicable context event\n' >&2
  exit 1
fi
"$binary" context refresh "$main_run" --workspace personal --socket "$socket_path" --idempotency-key live-context-refresh-noop --output json >"$scenario_root/noop-refresh.json"
grep -Fq '"status":"up_to_date"' "$scenario_root/noop-refresh.json"
grep -Fq '"event_sequence":0' "$scenario_root/noop-refresh.json"
scan_after_noop=$(sed -n 's/.*"scanned_through_event_sequence":\([0-9][0-9]*\).*/\1/p' "$scenario_root/noop-refresh.json" | sed -n '1p')
if [ -z "$scan_before_noop" ] || [ -z "$scan_after_noop" ] || [ "$scan_after_noop" -le "$scan_before_noop" ]
then
  printf 'no-op refresh cursor %s did not advance beyond %s\n' "$scan_after_noop" "$scan_before_noop" >&2
  exit 1
fi
if grep -Fq '"delta":' "$scenario_root/noop-refresh.json"
then
  printf 'no-op refresh created an empty delta\n' >&2
  exit 1
fi
"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events-after-noop.json"
cmp "$scenario_root/events-before-noop.json" "$scenario_root/events-after-noop.json"

"$binary" context delta list "$main_run" --workspace personal --after-sequence 0 --limit 2 --socket "$socket_path" --output json >"$scenario_root/deltas-page-one.json"
grep -Fq '"has_more":true' "$scenario_root/deltas-page-one.json"
grep -Fq '"next_sequence":2' "$scenario_root/deltas-page-one.json"
"$binary" context delta list "$main_run" --workspace personal --after-sequence 0 --limit 100 --socket "$socket_path" --output json >"$scenario_root/deltas-final.json"
grep -Fq '"delta_count":3' "$scenario_root/deltas-final.json"
grep -Fq '"last_acknowledged_sequence":3' "$scenario_root/deltas-final.json"

"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events-final.json"
for event_link in "$created_event_one:$delta_one" "$created_event_two:$(extract_id cdelta "$scenario_root/refresh-two.json")" "$created_event_three:$(extract_id cdelta "$scenario_root/refresh-three.json")"
do
  event_sequence=${event_link%%:*}
  event_delta=${event_link#*:}
  grep -Fq "\"sequence\":$event_sequence,\"type\":\"context_delta.built\"" "$scenario_root/events-final.json"
  grep -Fq "\"entity\":{\"type\":\"context_delta\",\"id\":\"$event_delta\"" "$scenario_root/events-final.json"
done
built_count=$(grep -o '"type":"context_delta.built"' "$scenario_root/events-final.json" | wc -l)
ack_count=$(grep -o '"type":"context_delta.acknowledged"' "$scenario_root/events-final.json" | wc -l)
if [ "$built_count" -ne 3 ] || [ "$ack_count" -ne 3 ]
then
  printf 'context delta event counts built=%s acknowledged=%s, want 3/3\n' "$built_count" "$ack_count" >&2
  exit 1
fi

stop_daemon
printf 'live context delta acceptance passed\n'
