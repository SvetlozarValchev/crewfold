#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-agent-messaging.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
fixture_root="$scenario_root/git-fixture"
daemon_log="$scenario_root/daemon.log"
daemon_pid=""
acceptance_runtime=${CREWFOLD_ACCEPTANCE_RUNTIME:-direct}
acceptance_provider=${CREWFOLD_ACCEPTANCE_PROVIDER:-fixture-mcp}
acceptance_wake=${CREWFOLD_ACCEPTANCE_WAKE:-failed}
acceptance_attach=${CREWFOLD_ACCEPTANCE_ATTACH:-true}

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
    printf 'agent messaging acceptance failed; collected diagnostics follow\n' >&2
    for diagnostic in "$daemon_log" "$scenario_root/inbox-before.json" "$scenario_root/inbox-after.json" "$scenario_root/requester-final.json" "$scenario_root/reviewer-final.json" "$scenario_root/thread.json" "$scenario_root/events.json"
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
  cleanup_runtime_processes
  if [ -d "$scenario_root" ]
  then
    find "$scenario_root" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >/dev/null

if [ -n "${CREWFOLD_ACCEPTANCE_HERDR_INCOMPATIBLE_SCHEMA:-}" ]
then
  "$binary" doctor --runtime herdr --output json >"$scenario_root/herdr-doctor.json"
  grep -Fq '"status":"ok"' "$scenario_root/herdr-doctor.json"
  if CREWFOLD_FIXTURE_HERDR_SCHEMA="$CREWFOLD_ACCEPTANCE_HERDR_INCOMPATIBLE_SCHEMA" "$binary" doctor --runtime herdr --output json >"$scenario_root/herdr-incompatible.json" 2>/dev/null
  then
    printf 'Herdr doctor accepted an incompatible recorded schema\n' >&2
    exit 1
  fi
  grep -Fq '"status":"failed"' "$scenario_root/herdr-incompatible.json"
  grep -Fq 'install a compatible Herdr release' "$scenario_root/herdr-incompatible.json"
fi

start_daemon() {
  "$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" 2>>"$daemon_log" &
  daemon_pid=$!
  attempts=0
  while ! "$binary" status --socket "$socket_path" --output json >"$scenario_root/daemon-status.json" 2>/dev/null
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

wait_for_question() {
  attempts=0
  while :
  do
    "$binary" inbox --workspace personal --agent reviewer --limit 20 --socket "$socket_path" --output json >"$scenario_root/inbox-before.json"
    if grep -Fq '"kind":"question"' "$scenario_root/inbox-before.json"
    then
      return
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 400 ]
    then
      printf 'timed out waiting for requester message\n' >&2
      exit 1
    fi
    sleep 0.025
  done
}

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key mailbox-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add demo --workspace personal --repo "$fixture_root/world-engine" --mode exclusive --socket "$socket_path" --idempotency-key mailbox-project --output json >"$scenario_root/project.json"
"$binary" checkout add demo "$fixture_root/world-engine-2" --workspace personal --mode exclusive --socket "$socket_path" --idempotency-key mailbox-reviewer-checkout --output json >"$scenario_root/checkout.json"
"$binary" agent create requester --workspace personal --role implementer --provider "$acceptance_provider" --runtime "$acceptance_runtime" --socket "$socket_path" --idempotency-key mailbox-requester-agent --output json >"$scenario_root/requester-agent.json"
"$binary" agent create reviewer --workspace personal --role reviewer --provider "$acceptance_provider" --runtime "$acceptance_runtime" --socket "$socket_path" --idempotency-key mailbox-reviewer-agent --output json >"$scenario_root/reviewer-agent.json"
"$binary" task create --workspace personal --project demo --title "Request contract review" --socket "$socket_path" --idempotency-key mailbox-requester-task --output json >"$scenario_root/requester-task.json"
"$binary" task create --workspace personal --project demo --title "Review contract request" --socket "$socket_path" --idempotency-key mailbox-reviewer-task --output json >"$scenario_root/reviewer-task.json"
requester_task=$(extract_id task "$scenario_root/requester-task.json")
reviewer_task=$(extract_id task "$scenario_root/reviewer-task.json")
"$binary" task assign "$requester_task" requester --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key mailbox-requester-assignment --output json >"$scenario_root/requester-assigned.json"
"$binary" task assign "$reviewer_task" reviewer --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key mailbox-reviewer-assignment --output json >"$scenario_root/reviewer-assigned.json"

"$binary" run start "$requester_task" --workspace personal --runtime "$acceptance_runtime" --provider "$acceptance_provider" --scenario "$repo_root/test/fixtures/agent-messaging/requester.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key mailbox-requester-run --output json >"$scenario_root/requester-run.json"
requester_run=$(extract_id run "$scenario_root/requester-run.json")
wait_for_question
grep -Fq '"status":"queued"' "$scenario_root/inbox-before.json"
grep -Fq '"wake_status":"not_requested"' "$scenario_root/inbox-before.json"
message_count=$(grep -o '"message":{"id":"msg_' "$scenario_root/inbox-before.json" | wc -l)
if [ "$message_count" -ne 1 ]
then
  printf 'denied or oversized probes created messages; count=%s\n' "$message_count" >&2
  exit 1
fi
thread_id=$(sed -n 's/.*"thread_id":"\(thread_[0-9a-f]*\)".*/\1/p' "$scenario_root/inbox-before.json" | sed -n '1p')
if [ -z "$thread_id" ]
then
  printf 'thread ID is missing from queued inbox\n' >&2
  exit 1
fi

stop_daemon
start_daemon
"$binary" inbox --workspace personal --agent reviewer --limit 20 --socket "$socket_path" --output json >"$scenario_root/inbox-after.json"
cmp "$scenario_root/inbox-before.json" "$scenario_root/inbox-after.json"
if [ "$acceptance_runtime" = "herdr" ] && { [ "$acceptance_attach" = "true" ] || [ "$acceptance_attach" = "lifecycle" ]; }
then
  "$binary" run attach "$requester_run" --workspace personal --socket "$socket_path" >"$scenario_root/attach-live.txt"
  grep -Fq 'fixture attached to term-' "$scenario_root/attach-live.txt"
fi

"$binary" run start "$reviewer_task" --workspace personal --checkout "$(extract_id co "$scenario_root/checkout.json")" --runtime "$acceptance_runtime" --provider "$acceptance_provider" --scenario "$repo_root/test/fixtures/agent-messaging/reviewer.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key mailbox-reviewer-run --output json >"$scenario_root/reviewer-run.json"
reviewer_run=$(extract_id run "$scenario_root/reviewer-run.json")
"$binary" run watch "$reviewer_run" --workspace personal --wait-seconds 15 --socket "$socket_path" --output json >"$scenario_root/reviewer-final.json"
"$binary" run watch "$requester_run" --workspace personal --wait-seconds 15 --socket "$socket_path" --output json >"$scenario_root/requester-final.json"
grep -Fq '"status":"completed"' "$scenario_root/reviewer-final.json"
grep -Fq '"status":"completed"' "$scenario_root/requester-final.json"

"$binary" thread show "$thread_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/thread.json"
grep -Fq 'Please verify the public contract' "$scenario_root/thread.json"
grep -Fq 'The public contract is consistent' "$scenario_root/thread.json"
ack_count=$(grep -o '"status":"acknowledged"' "$scenario_root/thread.json" | wc -l)
if [ "$ack_count" -ne 2 ]
then
  printf 'thread acknowledgements=%s, want 2\n' "$ack_count" >&2
  exit 1
fi
if [ "$acceptance_wake" = "succeeded" ]
then
  grep -Fq '"wake_status":"succeeded"' "$scenario_root/thread.json"
  grep -Fq '"delivered_run_id":"' "$scenario_root/thread.json"
else
  grep -Fq '"wake_status":"failed"' "$scenario_root/thread.json"
  grep -Fq 'runtime wake-up is unavailable' "$scenario_root/thread.json"
fi

"$binary" events list --workspace personal --after 0 --limit 400 --socket "$socket_path" --output json >"$scenario_root/events.json"
sent_count=$(grep -o '"type":"message.sent"' "$scenario_root/events.json" | wc -l)
if [ "$sent_count" -ne 2 ]
then
  printf 'durable message events=%s, want 2\n' "$sent_count" >&2
  exit 1
fi
if [ "$acceptance_wake" = "succeeded" ]
then
  grep -Fq '"type":"message.wake_succeeded"' "$scenario_root/events.json"
  grep -Fq '"type":"message.delivered"' "$scenario_root/events.json"
else
  grep -Fq '"type":"message.wake_failed"' "$scenario_root/events.json"
fi
grep -Fq '"type":"message.acknowledged"' "$scenario_root/events.json"

if [ "$acceptance_runtime" = "herdr" ] && { [ "$acceptance_attach" = "refused" ] || [ "$acceptance_attach" = "lifecycle" ]; }
then
  if "$binary" run attach "$requester_run" --workspace personal --socket "$socket_path" >"$scenario_root/attach.txt" 2>"$scenario_root/attach-error.txt"
  then
    printf 'terminal Herdr run unexpectedly remained attachable\n' >&2
    exit 1
  fi
  grep -Fq 'run status does not permit interactive runtime control' "$scenario_root/attach-error.txt"
fi

stop_daemon
printf 'Durable agent messaging acceptance: PASS\n'
