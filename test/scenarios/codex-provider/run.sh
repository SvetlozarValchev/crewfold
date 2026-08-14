#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-codex-provider.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
fixture_root="$scenario_root/git-fixture"
daemon_log="$scenario_root/daemon.log"
daemon_pid=''

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'Codex provider acceptance failed; collected diagnostics follow\n' >&2
    for diagnostic in "$daemon_log" "$scenario_root/doctor.json" "$scenario_root/final.json" "$scenario_root/logs.json"
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

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >/dev/null
export CREWFOLD_CODEX_BINARY="$repo_root/test/fixtures/providers/codex/codex"

"$binary" doctor --provider codex --output json >"$scenario_root/doctor.json"
grep -Fq '"status":"ok"' "$scenario_root/doctor.json"
grep -Fq '"mcp_client"' "$scenario_root/doctor.json"
if CREWFOLD_FIXTURE_CODEX_AUTH=missing "$binary" doctor --provider codex --output json >"$scenario_root/auth-failed.json" 2>/dev/null
then
  printf 'Codex doctor accepted a missing authentication fixture\n' >&2
  exit 1
fi
grep -Fq '"name":"authentication","status":"failed"' "$scenario_root/auth-failed.json"
grep -Fq 'Not logged in' "$scenario_root/auth-failed.json"

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

extract_id() {
  prefix=$1
  path=$2
  sed -n "s/.*\"id\":\"\(${prefix}_[0-9a-f]*\)\".*/\1/p" "$path" | sed -n '1p'
}

"$binary" workspace init personal --socket "$socket_path" --idempotency-key codex-provider-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add canary --workspace personal --repo "$fixture_root/world-engine" --mode exclusive --socket "$socket_path" --idempotency-key codex-provider-project --output json >"$scenario_root/project.json"
"$binary" agent create codex-worker --workspace personal --role implementer --provider codex --runtime direct --socket "$socket_path" --idempotency-key codex-provider-agent --output json >"$scenario_root/agent.json"
"$binary" task create --workspace personal --project canary --title "Recorded Codex work loop" --description "Use the scoped briefing and report the exact canary evidence." --socket "$socket_path" --idempotency-key codex-provider-task --output json >"$scenario_root/task.json"
task_id=$(extract_id task "$scenario_root/task.json")
"$binary" task assign "$task_id" codex-worker --lease-seconds 600 --workspace personal --expected-revision 1 --socket "$socket_path" --idempotency-key codex-provider-assignment --output json >"$scenario_root/assigned.json"
"$binary" run start "$task_id" --workspace personal --runtime direct --provider codex --scenario "$repo_root/test/fixtures/providers/codex/canary.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key codex-provider-run --output json >"$scenario_root/run.json"
run_id=$(extract_id run "$scenario_root/run.json")
"$binary" run watch "$run_id" --workspace personal --wait-seconds 15 --socket "$socket_path" --output json >"$scenario_root/final.json"
grep -Fq '"status":"completed"' "$scenario_root/final.json"
grep -Fq '"runtime":"direct"' "$scenario_root/final.json"
grep -Fq '"provider":"codex"' "$scenario_root/final.json"
grep -Fq '"scenario_name":"codex-canary"' "$scenario_root/final.json"
grep -Fq 'Recorded Codex endpoint completed the scoped work loop' "$scenario_root/final.json"
if grep -Eq '"(runtime_handle|provider_handle)"' "$scenario_root/final.json"
then
  printf 'Codex public run output exposed an opaque runtime/provider handle field\n' >&2
  exit 1
fi

"$binary" run logs "$run_id" --workspace personal --tail 100 --socket "$socket_path" --output json >"$scenario_root/logs.json"
grep -Fq 'codex-fixture-thread' "$scenario_root/logs.json"
if grep -Eq 'cf1\.run_[0-9a-f]{32}\.' "$scenario_root/logs.json"
then
  printf 'Codex runtime logs exposed a scoped capability token\n' >&2
  exit 1
fi

"$binary" events list --workspace personal --after 0 --limit 200 --socket "$socket_path" --output json >"$scenario_root/events.json"
grep -Fq '"type":"run.tool_called"' "$scenario_root/events.json"
grep -Fq '"type":"run.report_received"' "$scenario_root/events.json"

"$binary" daemon stop --socket "$socket_path" --output json >"$scenario_root/stop.json"
wait "$daemon_pid"
daemon_pid=''
printf 'Codex provider acceptance: PASS\n'
