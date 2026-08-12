#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-claude-provider.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
fixture_root="$scenario_root/git-fixture"
daemon_log="$scenario_root/daemon.log"
daemon_pid=''

cleanup() {
  status=$?
  if [ "$status" -ne 0 ] && [ -n "${claude_run_id:-}" ] && [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    "$binary" run logs "$claude_run_id" --workspace personal --tail 200 --socket "$socket_path" --output json >"$scenario_root/claude-logs.json" 2>/dev/null || true
  fi
  if [ "$status" -ne 0 ]
  then
    printf 'Claude provider acceptance failed; collected diagnostics follow\n' >&2
    for diagnostic in "$daemon_log" "$scenario_root/doctor.json" "$scenario_root/codex-final.json" "$scenario_root/claude-final.json" "$scenario_root/claude-logs.json"
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

extract_id() {
  prefix=$1
  path=$2
  sed -n "s/.*\"id\":\"\(${prefix}_[0-9a-f]*\)\".*/\1/p" "$path" | sed -n '1p'
}

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >/dev/null
export CREWFOLD_CODEX_BINARY="$repo_root/test/fixtures/providers/codex/codex"
export CREWFOLD_CLAUDE_BINARY="$repo_root/test/fixtures/providers/claude/claude"

"$binary" doctor --provider claude --output json >"$scenario_root/doctor.json"
grep -Fq '"status":"ok"' "$scenario_root/doctor.json"
grep -Fq '"compatibility","status":"ok"' "$scenario_root/doctor.json"
grep -Fq '"mcp_client"' "$scenario_root/doctor.json"
if CREWFOLD_FIXTURE_CLAUDE_AUTH=missing "$binary" doctor --provider claude --output json >"$scenario_root/auth-failed.json" 2>/dev/null
then
  printf 'Claude doctor accepted a missing authentication fixture\n' >&2
  exit 1
fi
grep -Fq '"name":"authentication","status":"failed"' "$scenario_root/auth-failed.json"
if CREWFOLD_FIXTURE_CLAUDE_VERSION=unsupported "$binary" doctor --provider claude --output json >"$scenario_root/version-failed.json" 2>/dev/null
then
  printf 'Claude doctor accepted an untested major version\n' >&2
  exit 1
fi
grep -Fq '"name":"compatibility","status":"failed"' "$scenario_root/version-failed.json"

"$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" 2>"$daemon_log" &
daemon_pid=$!
attempts=0
until "$binary" status --socket "$socket_path" --output json >"$scenario_root/status.json" 2>/dev/null
do
  if ! kill -0 "$daemon_pid" 2>/dev/null
  then
    wait "$daemon_pid" || true
    exit 1
  fi
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 300 ]
  then
    printf 'timed out waiting for the daemon\n' >&2
    exit 1
  fi
  sleep 0.01
done

"$binary" workspace init personal --socket "$socket_path" --idempotency-key claude-provider-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add canary --workspace personal --repo "$fixture_root/world-engine" --mode exclusive --socket "$socket_path" --idempotency-key claude-provider-project --output json >"$scenario_root/project.json"
"$binary" agent create codex-worker --workspace personal --role implementer --provider codex --runtime direct --socket "$socket_path" --idempotency-key provider-handoff-codex-agent --output json >"$scenario_root/codex-agent.json"
"$binary" agent create claude-worker --workspace personal --role implementer --provider claude --runtime direct --socket "$socket_path" --idempotency-key provider-handoff-claude-agent --output json >"$scenario_root/claude-agent.json"

"$binary" task create --workspace personal --project canary --title "Produce durable provider handoff" --description "Complete the first bounded step and send the continuation to claude-worker through Crewfold durable mail." --socket "$socket_path" --idempotency-key provider-handoff-codex-task --output json >"$scenario_root/codex-task.json"
codex_task_id=$(extract_id task "$scenario_root/codex-task.json")
"$binary" task assign "$codex_task_id" codex-worker --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key provider-handoff-codex-assignment --output json >"$scenario_root/codex-assigned.json"
"$binary" run start "$codex_task_id" --workspace personal --runtime direct --provider codex --scenario "$repo_root/test/fixtures/providers/codex/provider-handoff.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key provider-handoff-codex-run --output json >"$scenario_root/codex-run.json"
codex_run_id=$(extract_id run "$scenario_root/codex-run.json")
"$binary" run watch "$codex_run_id" --workspace personal --wait-seconds 15 --socket "$socket_path" --output json >"$scenario_root/codex-final.json"
grep -Fq '"status":"completed"' "$scenario_root/codex-final.json"
grep -Fq '"provider_handle":"codex-provider:v1:' "$scenario_root/codex-final.json"
grep -Fq 'durable mail' "$scenario_root/codex-final.json"

"$binary" task create --workspace personal --project canary --title "Continue from provider-neutral handoff" --description "Continue from the Crewfold-owned handoff in this agent inbox; do not use Codex transcript state." --socket "$socket_path" --idempotency-key provider-handoff-claude-task --output json >"$scenario_root/claude-task.json"
claude_task_id=$(extract_id task "$scenario_root/claude-task.json")
"$binary" task depend "$claude_task_id" --on "$codex_task_id" --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key provider-handoff-dependency --output json >"$scenario_root/claude-dependent.json"
"$binary" task assign "$claude_task_id" claude-worker --lease-seconds 600 --workspace personal --expected-revision 2 --socket "$socket_path" --idempotency-key provider-handoff-claude-assignment --output json >"$scenario_root/claude-assigned.json"
"$binary" run start "$claude_task_id" --workspace personal --runtime direct --provider claude --scenario "$repo_root/test/fixtures/providers/claude/provider-handoff.json" --expected-task-revision 3 --socket "$socket_path" --idempotency-key provider-handoff-claude-run --output json >"$scenario_root/claude-run.json"
claude_run_id=$(extract_id run "$scenario_root/claude-run.json")
"$binary" run watch "$claude_run_id" --workspace personal --wait-seconds 15 --socket "$socket_path" --output json >"$scenario_root/claude-final.json"
grep -Fq '"status":"completed"' "$scenario_root/claude-final.json"
grep -Fq '"provider_handle":"claude-provider:v1:' "$scenario_root/claude-final.json"
grep -Fq 'durable mail without provider-private transcript state' "$scenario_root/claude-final.json"

"$binary" run logs "$codex_run_id" --workspace personal --tail 100 --socket "$socket_path" --output json >"$scenario_root/codex-logs.json"
"$binary" run logs "$claude_run_id" --workspace personal --tail 100 --socket "$socket_path" --output json >"$scenario_root/claude-logs.json"
grep -Fq 'codex-fixture-thread' "$scenario_root/codex-logs.json"
grep -Fq 'claude-fixture-session' "$scenario_root/claude-logs.json"
if grep -Fq 'codex-fixture-thread' "$scenario_root/claude-logs.json" || grep -Fq 'claude-fixture-session' "$scenario_root/codex-logs.json"
then
  printf 'provider-private session state crossed the durable handoff boundary\n' >&2
  exit 1
fi
if grep -Eq 'cf1\.run_[0-9a-f]{32}\.' "$scenario_root/codex-logs.json" "$scenario_root/claude-logs.json"
then
  printf 'provider runtime logs exposed a scoped capability token\n' >&2
  exit 1
fi

context_id=$(sed -n 's/.*"context_packet_id":"\(ctx_[0-9a-f]*\)".*/\1/p' "$scenario_root/claude-final.json" | sed -n '1p')
"$binary" context show "$context_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/claude-context.json"
grep -Fq '"section":"transcripts","reason":"raw provider transcripts are not context authority"' "$scenario_root/claude-context.json"
grep -Fq 'Durable provider handoff' "$scenario_root/claude-context.json"

"$binary" events list --after 0 --limit 500 --socket "$socket_path" --output json >"$scenario_root/events.json"
grep -Fq '"type":"message.sent"' "$scenario_root/events.json"
grep -Fq '"type":"run.tool_called"' "$scenario_root/events.json"
grep -Fq '"type":"run.report_received"' "$scenario_root/events.json"

"$binary" daemon stop --socket "$socket_path" --output json >"$scenario_root/stop.json"
wait "$daemon_pid"
daemon_pid=''
printf 'Claude provider and cross-provider handoff acceptance: PASS\n'
