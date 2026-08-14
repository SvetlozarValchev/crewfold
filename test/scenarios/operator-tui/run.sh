#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-operator-tui.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
fixture_root="$scenario_root/git-fixture"
fixture_state="$scenario_root/herdr-fixture"
daemon_log="$scenario_root/daemon.log"
ui_transcript="$scenario_root/ui.typescript"
ui_stdout="$scenario_root/ui.stdout"
ui_input="$scenario_root/ui.input"
daemon_pid=""
ui_pid=""
ui_fd_open=false

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'operator TUI acceptance failed; diagnostics follow\n' >&2
    if [ -f "$daemon_log" ]
    then
      tail -n 240 "$daemon_log" >&2
    fi
    if [ -f "$ui_transcript" ]
    then
      tail -c 24000 "$ui_transcript" >&2
    fi
    for artifact in run-completed.json events-after-resume.json
    do
      if [ -f "$scenario_root/$artifact" ]
      then
        printf '\n%s:\n' "$artifact" >&2
        sed -n '1,80p' "$scenario_root/$artifact" >&2
      fi
    done
  fi
  if [ "$ui_fd_open" = true ]
  then
    exec 3>&-
    ui_fd_open=false
  fi
  if [ -n "$ui_pid" ] && kill -0 "$ui_pid" 2>/dev/null
  then
    kill "$ui_pid" 2>/dev/null || true
    wait "$ui_pid" 2>/dev/null || true
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
  while ! "$binary" status --socket "$socket_path" --output json >"$scenario_root/daemon-status.json" 2>/dev/null
  do
    if ! kill -0 "$daemon_pid" 2>/dev/null
    then
      wait "$daemon_pid" || true
      sed -n '1,240p' "$daemon_log" >&2
      exit 1
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 400 ]
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

wait_for_run_status() {
  run_id=$1
  wanted=$2
  output=$3
  attempts=0
  while :
  do
    "$binary" run show "$run_id" --workspace personal --socket "$socket_path" --output json >"$output"
    if grep -Fq "\"status\":\"$wanted\"" "$output"
    then
      return
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 600 ]
    then
      printf 'timed out waiting for run %s status %s\n' "$run_id" "$wanted" >&2
      sed -n '1,100p' "$output" >&2
      exit 1
    fi
    sleep 0.02
  done
}

wait_for_event_quiescence() {
  output=$1
  previous=""
  stable=0
  attempts=0
  while [ "$stable" -lt 3 ]
  do
    "$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$output"
    high_water=$(sed -n 's/.*"high_water":\([0-9][0-9]*\).*/\1/p' "$output" | sed -n '1p')
    if [ -n "$high_water" ] && [ "$high_water" = "$previous" ]
    then
      stable=$((stable + 1))
    else
      stable=0
      previous=$high_water
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 200 ]
    then
      printf 'event high-water did not become quiescent\n' >&2
      exit 1
    fi
    sleep 0.05
  done
}

transcript_size() {
  wc -c <"$ui_transcript" | tr -d ' '
}

wait_for_ui_text_after() {
  offset=$1
  pattern=$2
  attempts=0
  while :
  do
    if tail -c "+$((offset + 1))" "$ui_transcript" 2>/dev/null | grep -aFq "$pattern"
    then
      return
    fi
    if [ -n "$ui_pid" ] && ! kill -0 "$ui_pid" 2>/dev/null
    then
      wait "$ui_pid" || true
      ui_pid=""
      printf 'operator UI exited while waiting for %s\n' "$pattern" >&2
      exit 1
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 600 ]
    then
      printf 'timed out waiting for operator UI text: %s\n' "$pattern" >&2
      exit 1
    fi
    sleep 0.02
  done
}

send_ui_text() {
  printf '%s' "$1" >&3
}

send_ctrl_enter() {
  printf '\033[13;5u' >&3
}

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -trimpath -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >/dev/null
mkdir -p "$fixture_state"

export CREWFOLD_HERDR_BINARY="$repo_root/test/fixtures/runtimes/herdr/herdr"
export CREWFOLD_HERDR_SESSION=crewfold-operator-tui
export CREWFOLD_FIXTURE_HERDR_STATE="$fixture_state"
export CREWFOLD_FIXTURE_HERDR_SCHEMA="$repo_root/test/fixtures/protocol/herdr/schema-compatible.json"

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key operator-tui-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add demo --workspace personal --repo "$fixture_root/world-engine" --mode exclusive --socket "$socket_path" --idempotency-key operator-tui-project --output json >"$scenario_root/project.json"
checkout_id=$(extract_id co "$scenario_root/project.json")
"$binary" agent create implementer --workspace personal --role 'arbitrary descriptive role only' --provider fixture-terminal --runtime herdr --max-concurrency 2 --socket "$socket_path" --idempotency-key operator-tui-agent --output json >"$scenario_root/agent.json"
"$binary" task create --workspace personal --project demo --title 'Resolve the exact operator choice' --priority 900 --socket "$socket_path" --idempotency-key operator-tui-task --output json >"$scenario_root/task.json"
task_id=$(extract_id task "$scenario_root/task.json")
"$binary" task assign "$task_id" implementer --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key operator-tui-assignment --output json >"$scenario_root/assignment.json"
"$binary" run start "$task_id" --workspace personal --checkout "$checkout_id" --runtime herdr --provider fixture-terminal --scenario "$repo_root/test/scenarios/operator-tui/blocked.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key operator-tui-run --output json >"$scenario_root/run.json"
run_id=$(extract_id run "$scenario_root/run.json")
if [ -z "$checkout_id" ] || [ -z "$task_id" ] || [ -z "$run_id" ]
then
  printf 'operator fixture IDs were not returned\n' >&2
  exit 1
fi
wait_for_run_status "$run_id" blocked "$scenario_root/run-blocked.json"
grep -Fq 'Which gameplay loop should we implement first?' "$scenario_root/run-blocked.json"
"$binary" run list --workspace personal --project demo --socket "$socket_path" --output json >"$scenario_root/runs-blocked.json"
grep -Fq '"can_attach":true' "$scenario_root/runs-blocked.json"

wait_for_event_quiescence "$scenario_root/events-before-ui.json"
event_high_water=$(sed -n 's/.*"high_water":\([0-9][0-9]*\).*/\1/p' "$scenario_root/events-before-ui.json" | sed -n '1p')
"$binary" briefing show --workspace personal --project demo --socket "$socket_path" --output json >"$scenario_root/briefing.json"
briefing_hash=$(sed -n 's/.*"content_sha256":"\([0-9a-f][0-9a-f]*\)".*/\1/p' "$scenario_root/briefing.json" | sed -n '1p')
if [ "${#briefing_hash}" -ne 64 ] ||
  ! grep -Fq "\"event_cursor\":$event_high_water" "$scenario_root/briefing.json" ||
  ! grep -Fq "\"cutoff_event_sequence\":$event_high_water" "$scenario_root/briefing.json" ||
  ! grep -Fq '"caught_up":true' "$scenario_root/briefing.json"
then
  printf 'canonical briefing did not return the complete current cut and SHA-256\n' >&2
  exit 1
fi

mkfifo "$ui_input"
exec 3<>"$ui_input"
ui_fd_open=true
ui_command="stty rows 40 cols 180; exec env NO_COLOR=1 TERM=xterm-256color $binary ui --socket $socket_path --workspace personal --project demo --color never"
script -qefc "$ui_command" "$ui_transcript" <"$ui_input" >"$ui_stdout" 2>&1 &
ui_pid=$!

wait_for_ui_text_after 0 'Canonical state synchronized'
wait_for_ui_text_after 0 '| live |'

# The first navigation step opens Briefing. Its selected canonical briefing
# renders the same complete content hash returned by briefing.show.
briefing_mark=$(transcript_size)
send_ui_text j
wait_for_ui_text_after "$briefing_mark" 'Briefing at event'
send_ui_text "$(printf '\r')"
wait_for_ui_text_after "$briefing_mark" "Event cursor: $event_high_water"
wait_for_ui_text_after "$briefing_mark" "Cutoff event sequence: $event_high_water"
wait_for_ui_text_after "$briefing_mark" 'Content SHA-256: '
if ! tail -c "+$((briefing_mark + 1))" "$ui_transcript" | grep -aEq 'Content SHA-256: [0-9a-f]{64}'
then
  printf 'operator UI did not render the complete canonical briefing SHA-256\n' >&2
  exit 1
fi

# Return to navigation, open Work, and select the sole blocked run. Enter only
# changes focus/inspection; it is not a mutation.
navigation_mark=$(transcript_size)
send_ui_text "$(printf '\033')"
wait_for_ui_text_after "$navigation_mark" 'navigation | cursor'
work_mark=$(transcript_size)
send_ui_text j
wait_for_ui_text_after "$work_mark" 'Resolve the exact operator choice'
send_ui_text "$(printf '\r')"
sleep 0.1
run_mark=$(transcript_size)
send_ui_text j
wait_for_ui_text_after "$run_mark" 'Which gameplay loop should we implement first?'

# Ordinary attach is reviewed and confirmed, suspends Bubble Tea, runs the exact
# recorded Herdr attach process, and restores the same selected dashboard.
attach_mark=$(transcript_size)
send_ui_text a
wait_for_ui_text_after "$attach_mark" 'Available actions'
send_ui_text "$(printf '\r')"
wait_for_ui_text_after "$attach_mark" 'Action: Attach to run'
send_ctrl_enter
wait_for_ui_text_after "$attach_mark" 'fixture attached to term-'
# Returning from the child immediately enters the canonical resynchronization
# path; the UI may replace the transient return status before the PTY observer
# samples it. The durable proof is the child output followed by a fresh live
# canonical frame in the same process with the reviewed run still selected.
wait_for_ui_text_after "$attach_mark" 'Canonical state synchronized'
wait_for_ui_text_after "$attach_mark" '| live |'
wait_for_ui_text_after "$attach_mark" "$run_id"

"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events-after-attach.json"
if ! cmp -s "$scenario_root/events-before-ui.json" "$scenario_root/events-after-attach.json"
then
  printf 'dashboard navigation, briefing inspection, or ordinary attach appended an event\n' >&2
  exit 1
fi

# The dashboard does not own daemon lifetime. Losing the daemon keeps the
# selected cached record visible and labels it stale; restarting the same daemon
# returns to a live synchronized projection without relaunching the UI.
reconnect_mark=$(transcript_size)
stop_daemon
wait_for_ui_text_after "$reconnect_mark" 'reconnecting (cached state is stale)'
start_daemon
# Bubble Tea emits a terminal diff. The footer text can remain unchanged from
# the pre-loss live frame, so recovery may redraw only the header's `live`
# connection label.
wait_for_ui_text_after "$reconnect_mark" 'live '

# Resume is the one durable UI mutation. It requires selecting the second typed
# action, reviewing the exact run revision/consequence, and Ctrl+Enter. The
# canonical event journal proves the mutation happened exactly once.
resume_mark=$(transcript_size)
send_ui_text a
wait_for_ui_text_after "$resume_mark" 'Available actions'
send_ui_text j
sleep 0.1
send_ui_text "$(printf '\r')"
wait_for_ui_text_after "$resume_mark" 'Action: Resume run'
wait_for_ui_text_after "$resume_mark" "Target: run / $run_id"
send_ctrl_enter
wait_for_run_status "$run_id" completed "$scenario_root/run-completed.json"
grep -Fq '"step_cursor":3' "$scenario_root/run-completed.json"

"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events-after-resume.json"
resume_events=$(grep -o '"type":"run.resumed"' "$scenario_root/events-after-resume.json" | wc -l | tr -d ' ')
if [ "$resume_events" -ne 1 ]
then
  printf 'run.resumed event count=%s, want exactly 1\n' "$resume_events" >&2
  exit 1
fi

wait_for_ui_text_after "$resume_mark" 'Result: selected gameplay loop implemented'
send_ui_text q
exec 3>&-
ui_fd_open=false
if ! wait "$ui_pid"
then
  ui_pid=""
  printf 'operator UI process exited unsuccessfully\n' >&2
  exit 1
fi
ui_pid=""

stop_daemon
printf 'Provider-free operator TUI acceptance: PASS\n'
