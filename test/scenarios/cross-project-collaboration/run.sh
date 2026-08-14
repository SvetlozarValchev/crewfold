#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-cross-project-collaboration.XXXXXX")
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
    printf 'cross-project collaboration acceptance failed; collected diagnostics follow\n' >&2
    for diagnostic in "$daemon_log" "$scenario_root/thread.json" "$scenario_root/participants.json" "$scenario_root/plug-context.json" "$scenario_root/wrong-final.json" "$scenario_root/engine-final.json" "$scenario_root/plug-final.json" "$scenario_root/events.json" "$scenario_root/stale-invite.json"
    do
      if [ -f "$diagnostic" ]
      then
        printf '%s\n' "$diagnostic" >&2
        sed -n '1,260p' "$diagnostic" >&2
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
      sed -n '1,260p' "$daemon_log" >&2
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

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
mkdir -p "$repos_root"
init_repository "$repos_root/plugandrev" plugandrev
init_repository "$repos_root/engine-sim-offline" engine-sim-offline
init_repository "$repos_root/integration-review" integration-review

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key collaboration-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add plugandrev --workspace personal --repo "$repos_root/plugandrev" --mode exclusive --socket "$socket_path" --idempotency-key collaboration-plug-project --output json >"$scenario_root/plug-project.json"
"$binary" project add engine-sim-offline --workspace personal --repo "$repos_root/engine-sim-offline" --mode exclusive --socket "$socket_path" --idempotency-key collaboration-engine-project --output json >"$scenario_root/engine-project.json"
"$binary" project add integration-review --workspace personal --repo "$repos_root/integration-review" --mode exclusive --socket "$socket_path" --idempotency-key collaboration-review-project --output json >"$scenario_root/review-project.json"
plug_project=$(extract_id prj "$scenario_root/plug-project.json")
engine_project=$(extract_id prj "$scenario_root/engine-project.json")

"$binary" agent create plug-agent --workspace personal --role application-integrator --provider fixture-mcp --runtime direct --socket "$socket_path" --idempotency-key collaboration-plug-agent --output json >"$scenario_root/plug-agent.json"
"$binary" agent create engine-agent --workspace personal --role engine-maintainer --provider fixture-mcp --runtime direct --socket "$socket_path" --idempotency-key collaboration-engine-agent --output json >"$scenario_root/engine-agent.json"
"$binary" agent create review-agent --workspace personal --role integration-reviewer --provider fixture-mcp --runtime direct --socket "$socket_path" --idempotency-key collaboration-review-agent --output json >"$scenario_root/review-agent.json"
"$binary" agent create outsider-agent --workspace personal --role unrelated-worker --provider fixture-mcp --runtime direct --socket "$socket_path" --idempotency-key collaboration-outsider-agent --output json >"$scenario_root/outsider-agent.json"

"$binary" task create --workspace personal --project plugandrev --title "Integrate offline engine" --socket "$socket_path" --idempotency-key collaboration-plug-task --output json >"$scenario_root/plug-task.json"
"$binary" task create --workspace personal --project plugandrev --title "Unrelated plug cleanup" --socket "$socket_path" --idempotency-key collaboration-wrong-task --output json >"$scenario_root/wrong-task.json"
"$binary" task create --workspace personal --project engine-sim-offline --title "Explain engine contract" --socket "$socket_path" --idempotency-key collaboration-engine-task --output json >"$scenario_root/engine-task.json"
"$binary" task create --workspace personal --project integration-review --title "Review the integration" --socket "$socket_path" --idempotency-key collaboration-review-task --output json >"$scenario_root/review-task.json"
"$binary" task create --workspace personal --project integration-review --title "Unrelated outsider work" --socket "$socket_path" --idempotency-key collaboration-outsider-task --output json >"$scenario_root/outsider-task.json"
plug_task=$(extract_id task "$scenario_root/plug-task.json")
wrong_task=$(extract_id task "$scenario_root/wrong-task.json")
engine_task=$(extract_id task "$scenario_root/engine-task.json")
review_task=$(extract_id task "$scenario_root/review-task.json")
outsider_task=$(extract_id task "$scenario_root/outsider-task.json")

"$binary" task assign "$plug_task" plug-agent --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key collaboration-plug-assignment --output json >"$scenario_root/plug-assigned.json"
"$binary" task assign "$wrong_task" plug-agent --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key collaboration-wrong-assignment --output json >"$scenario_root/wrong-assigned.json"
"$binary" task assign "$engine_task" engine-agent --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key collaboration-engine-assignment --output json >"$scenario_root/engine-assigned.json"
"$binary" task assign "$review_task" review-agent --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key collaboration-review-assignment --output json >"$scenario_root/review-assigned.json"
"$binary" task assign "$outsider_task" outsider-agent --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key collaboration-outsider-assignment --output json >"$scenario_root/outsider-assigned.json"

# Ordinary direct mail keeps its project boundary even when the recipient agent
# also owns a task in another project.
"$binary" message send plug-agent --workspace personal --project engine-sim-offline --kind inform --body "project-scoped direct mail must remain in the engine project" --socket "$socket_path" --idempotency-key collaboration-direct-message --output json >"$scenario_root/direct-message.json"
direct_thread=$(extract_id thread "$scenario_root/direct-message.json")

"$binary" thread create --workspace personal --subject "plugandrev / engine-sim-offline contract" --participant "plug-agent=$plug_task" --participant "engine-agent=$engine_task" --socket "$socket_path" --idempotency-key collaboration-thread --output json >"$scenario_root/thread-created.json"
thread_id=$(extract_id thread "$scenario_root/thread-created.json")
if [ -z "$thread_id" ]
then
  printf 'participant thread ID is missing\n' >&2
  exit 1
fi
sed "s/THREAD_ID/$thread_id/g" "$repo_root/test/fixtures/cross-project-collaboration/engine.json.in" >"$scenario_root/engine.json"

# A live run for the same agent but a different task must not be selected for
# wake-up or see the participant message while polling its MCP inbox.
"$binary" run start "$wrong_task" --workspace personal --runtime direct --provider fixture-mcp --scenario "$repo_root/test/fixtures/cross-project-collaboration/wrong-task.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key collaboration-wrong-run --output json >"$scenario_root/wrong-run.json"
wrong_run=$(extract_id run "$scenario_root/wrong-run.json")
attempts=0
while :
do
  "$binary" run show "$wrong_run" --workspace personal --socket "$socket_path" --output json >"$scenario_root/wrong-active.json"
  if grep -Fq '"status":"active"' "$scenario_root/wrong-active.json"
  then
    break
  fi
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 200 ]
  then
    printf 'wrong-task run never became active\n' >&2
    exit 1
  fi
  sleep 0.01
done

"$binary" run start "$engine_task" --workspace personal --runtime direct --provider fixture-mcp --scenario "$scenario_root/engine.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key collaboration-engine-run --output json >"$scenario_root/engine-run.json"
engine_run=$(extract_id run "$scenario_root/engine-run.json")
attempts=0
while :
do
  "$binary" thread show "$thread_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/thread-before-restart.json"
  if grep -Fq 'engine-sim-offline exposes deterministic step' "$scenario_root/thread-before-restart.json"
  then
    break
  fi
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 300 ]
  then
    printf 'engine request was not durably queued\n' >&2
    exit 1
  fi
  sleep 0.02
done
"$binary" run watch "$wrong_run" --workspace personal --wait-seconds 8 --socket "$socket_path" --output json >"$scenario_root/wrong-final.json"
grep -Fq '"status":"failed"' "$scenario_root/wrong-final.json"

grep -Fq '"wake_status":"not_requested"' "$scenario_root/thread-before-restart.json"

stop_daemon
start_daemon

# Roster growth is owner-only and optimistic. A stale second invite cannot alter
# the participant set.
"$binary" thread invite "$thread_id" --workspace personal --agent review-agent --task "$review_task" --expected-participant-revision 1 --socket "$socket_path" --idempotency-key collaboration-review-invite --output json >"$scenario_root/invited.json"
grep -Fq '"participant_revision":2' "$scenario_root/invited.json"
if "$binary" thread invite "$thread_id" --workspace personal --agent outsider-agent --task "$outsider_task" --expected-participant-revision 1 --socket "$socket_path" --idempotency-key collaboration-stale-invite --output json >"$scenario_root/stale-invite.json" 2>&1
then
  printf 'stale participant invite unexpectedly succeeded\n' >&2
  exit 1
fi
grep -Fq 'revision_conflict' "$scenario_root/stale-invite.json"
"$binary" thread participants "$thread_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/participants.json"
participant_count=$(grep -o '"id":"participant_' "$scenario_root/participants.json" | wc -l)
if [ "$participant_count" -ne 3 ]
then
  printf 'participant count=%s, want 3 after rejected stale invite\n' "$participant_count" >&2
  exit 1
fi

"$binary" context build "$plug_task" --workspace personal --agent plug-agent --expected-task-revision 2 --socket "$socket_path" --idempotency-key collaboration-plug-context --output json >"$scenario_root/plug-context.json"
plug_context=$(extract_id ctx "$scenario_root/plug-context.json")
grep -Fq 'urn:crewfold:schema:domain:context-packet:v1' "$scenario_root/plug-context.json"
grep -Fq 'engine-sim-offline exposes deterministic step' "$scenario_root/plug-context.json"
if grep -Fq 'project-scoped direct mail must remain' "$scenario_root/plug-context.json"
then
  printf 'cross-project direct mail leaked into the plug task context\n' >&2
  exit 1
fi

"$binary" run start "$plug_task" --workspace personal --context "$plug_context" --runtime direct --provider fixture-mcp --scenario "$repo_root/test/fixtures/cross-project-collaboration/plug.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key collaboration-plug-run --output json >"$scenario_root/plug-run.json"
plug_run=$(extract_id run "$scenario_root/plug-run.json")
"$binary" run watch "$plug_run" --workspace personal --wait-seconds 12 --socket "$socket_path" --output json >"$scenario_root/plug-final.json"
grep -Fq '"status":"completed"' "$scenario_root/plug-final.json"
"$binary" run watch "$engine_run" --workspace personal --wait-seconds 12 --socket "$socket_path" --output json >"$scenario_root/engine-final.json"
grep -Fq '"status":"completed"' "$scenario_root/engine-final.json"

"$binary" thread show "$thread_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/thread.json"
grep -Fq 'engine-sim-offline exposes deterministic step' "$scenario_root/thread.json"
grep -Fq 'plugandrev needs stable snapshot IDs' "$scenario_root/thread.json"
grep -Fq "\"project_id\":\"$engine_project\"" "$scenario_root/thread.json"
grep -Fq "\"task_id\":\"$engine_task\"" "$scenario_root/thread.json"
grep -Fq "\"project_id\":\"$plug_project\"" "$scenario_root/thread.json"
grep -Fq "\"task_id\":\"$plug_task\"" "$scenario_root/thread.json"
recipient_count=$(grep -o '"message_id":"msg_' "$scenario_root/thread.json" | wc -l)
if [ "$recipient_count" -ne 2 ]
then
  printf 'recipient records=%s, want one for each of two messages\n' "$recipient_count" >&2
  exit 1
fi
acknowledged_count=$(grep -o '"status":"acknowledged"' "$scenario_root/thread.json" | wc -l)
if [ "$acknowledged_count" -ne 2 ]
then
  printf 'acknowledged messages=%s, want 2\n' "$acknowledged_count" >&2
  exit 1
fi

"$binary" inbox --workspace personal --agent review-agent --limit 20 --socket "$socket_path" --output json >"$scenario_root/reviewer-inbox.json"
grep -Fq '"items":[]' "$scenario_root/reviewer-inbox.json"
"$binary" thread show "$direct_thread" --workspace personal --socket "$socket_path" --output json >"$scenario_root/direct-thread.json"
grep -Fq 'project-scoped direct mail must remain in the engine project' "$scenario_root/direct-thread.json"
grep -Fq '"status":"queued"' "$scenario_root/direct-thread.json"

"$binary" knowledge list --workspace personal --project plugandrev --socket "$socket_path" --output json >"$scenario_root/plug-knowledge.json"
"$binary" knowledge list --workspace personal --project engine-sim-offline --socket "$socket_path" --output json >"$scenario_root/engine-knowledge.json"
grep -Fq '"revisions":[]' "$scenario_root/plug-knowledge.json"
grep -Fq '"revisions":[]' "$scenario_root/engine-knowledge.json"

"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events.json"
grep -Fq '"type":"thread.participant_added"' "$scenario_root/events.json"
grep -Fq '"origin_project_id"' "$scenario_root/events.json"
for forbidden_event in 'knowledge.' 'task.dependency_added' 'claim.' 'meeting.'
do
  if grep -Fq "\"type\":\"$forbidden_event" "$scenario_root/events.json"
  then
    printf 'collaboration produced forbidden mutation event prefix %s\n' "$forbidden_event" >&2
    exit 1
  fi
done

stop_daemon
printf 'Participant-bound cross-project collaboration acceptance: PASS\n'
