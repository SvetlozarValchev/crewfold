#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)

if [ "${CREWFOLD_LIVE_CODEX:-}" != "1" ]
then
  printf 'Installed Codex canary: SKIP (set CREWFOLD_LIVE_CODEX=1)\n'
  exit 0
fi
if [ "${CREWFOLD_ALLOW_MODEL_CALLS:-}" != "1" ]
then
  printf 'Installed Codex canary: REFUSED (also set CREWFOLD_ALLOW_MODEL_CALLS=1 to acknowledge network and model usage)\n' >&2
  exit 1
fi
if ! command -v codex >/dev/null 2>&1
then
  printf 'Installed Codex canary: SKIP (codex is not installed)\n'
  exit 0
fi
if ! command -v herdr >/dev/null 2>&1
then
  printf 'Installed Codex canary: SKIP (herdr is not installed)\n'
  exit 0
fi
codex_sandbox=${CREWFOLD_LIVE_CODEX_SANDBOX:-workspace-write}
codex_external_sandbox=false
case "$codex_sandbox" in
  workspace-write) ;;
  danger-full-access)
    if [ "${CREWFOLD_EXTERNAL_CODEX_SANDBOX:-}" != "1" ]
    then
      printf 'Installed Codex canary: REFUSED (danger-full-access requires CREWFOLD_EXTERNAL_CODEX_SANDBOX=1 and an independently enforced boundary)\n' >&2
      exit 1
    fi
    codex_external_sandbox=true
    ;;
  *)
    printf 'Installed Codex canary: REFUSED (unsupported CREWFOLD_LIVE_CODEX_SANDBOX value)\n' >&2
    exit 1
    ;;
esac

go_runner="$repo_root/scripts/go.sh"
live_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-live-codex.XXXXXX")
binary="$live_root/crewfold"
data_dir="$live_root/data"
socket_path="$live_root/crewfold.sock"
project_path="$live_root/canary-project"
daemon_log="$live_root/daemon.log"
server_log="$live_root/herdr-server.log"
session="crewfold-codex-live-$$"
daemon_pid=''
server_pid=''

cleanup() {
  status=$?
  if [ "$status" -ne 0 ] && [ -n "${run_id:-}" ] && [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    "$binary" run logs "$run_id" --workspace canary --tail 200 --socket "$socket_path" --output json >"$live_root/logs.json" 2>/dev/null || true
  fi
  if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    kill "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  HERDR_SESSION="$session" herdr server stop >/dev/null 2>&1 || true
  if [ -n "$server_pid" ] && kill -0 "$server_pid" 2>/dev/null
  then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  herdr session delete "$session" >/dev/null 2>&1 || true
  if [ "$status" -ne 0 ]
  then
    printf 'Installed Codex canary failed; collected diagnostics follow\n' >&2
    for diagnostic in "$daemon_log" "$server_log" "$live_root/codex-doctor.json" "$live_root/final.json" "$live_root/logs.json"
    do
      if [ -f "$diagnostic" ]
      then
        printf '%s\n' "$diagnostic" >&2
        sed -n '1,240p' "$diagnostic" >&2
      fi
    done
  fi
  if [ -d "$live_root" ]
  then
    find "$live_root" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
mkdir -p "$project_path"
git -C "$project_path" init -q
git -C "$project_path" config user.name 'Crewfold Canary'
git -C "$project_path" config user.email 'crewfold-canary@invalid.example'
printf 'before\n' >"$project_path/answer.txt"
printf '%s\n' '#!/bin/sh' 'set -eu' 'test "$(cat answer.txt)" = "crewfold codex canary"' >"$project_path/check.sh"
chmod +x "$project_path/check.sh"
git -C "$project_path" add answer.txt check.sh
git -C "$project_path" commit -q -m 'Create disposable Codex canary'

HERDR_SESSION="$session" herdr server >"$server_log" 2>&1 &
server_pid=$!
attempts=0
until HERDR_SESSION="$session" herdr api snapshot >/dev/null 2>&1
do
  if ! kill -0 "$server_pid" 2>/dev/null
  then
    wait "$server_pid" || true
    printf 'dedicated Herdr server exited during startup\n' >&2
    exit 1
  fi
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 200 ]
  then
    printf 'timed out waiting for the dedicated Herdr session\n' >&2
    exit 1
  fi
  sleep 0.025
done

export CREWFOLD_HERDR_BINARY=$(command -v herdr)
export CREWFOLD_HERDR_SESSION="$session"
if [ -z "${CREWFOLD_CODEX_BINARY:-}" ]
then
  export CREWFOLD_CODEX_BINARY=$(command -v codex)
fi
"$binary" doctor --runtime herdr --output json >"$live_root/herdr-doctor.json"
"$binary" doctor --provider codex --output json >"$live_root/codex-doctor.json"

"$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" --codex-sandbox "$codex_sandbox" --codex-external-sandbox "$codex_external_sandbox" --codex-tool-network-access true 2>"$daemon_log" &
daemon_pid=$!
attempts=0
until "$binary" status --socket "$socket_path" --output json >"$live_root/status.json" 2>/dev/null
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

"$binary" workspace init canary --socket "$socket_path" --idempotency-key live-codex-workspace --output json >"$live_root/workspace.json"
"$binary" project add canary --workspace canary --repo "$project_path" --mode exclusive --socket "$socket_path" --idempotency-key live-codex-project --output json >"$live_root/project.json"
"$binary" agent create implementer --workspace canary --role implementer --provider codex --runtime herdr --socket "$socket_path" --idempotency-key live-codex-agent --output json >"$live_root/agent.json"
"$binary" task create --workspace canary --project canary --title "Change one canary file" --description "Change answer.txt so its only content is exactly 'crewfold codex canary' followed by a newline. Run ./check.sh and git diff --check. Do not modify check.sh, commit, push, use the network for the task, or access any path outside this disposable checkout. Report completion through Crewfold MCP with evidence IDs implementation_complete and tests_passed." --socket "$socket_path" --idempotency-key live-codex-task --output json >"$live_root/task.json"
task_id=$(extract_id task "$live_root/task.json")
"$binary" task assign "$task_id" implementer --lease-seconds 1200 --workspace canary --expected-revision 1 --socket "$socket_path" --idempotency-key live-codex-assignment --output json >"$live_root/assigned.json"
"$binary" run start "$task_id" --workspace canary --runtime herdr --provider codex --scenario "$repo_root/test/fixtures/providers/codex/canary.json" --expected-task-revision 2 --socket "$socket_path" --idempotency-key live-codex-run --output json >"$live_root/run.json"
run_id=$(extract_id run "$live_root/run.json")
"$binary" run watch "$run_id" --workspace canary --wait-seconds 600 --socket "$socket_path" --output json >"$live_root/final.json"
grep -Fq '"status":"completed"' "$live_root/final.json"

(cd "$project_path" && ./check.sh && git diff --check)
changed_paths=$(git -C "$project_path" diff --name-only)
if [ "$changed_paths" != "answer.txt" ]
then
  printf 'live Codex changed unexpected paths: %s\n' "$changed_paths" >&2
  exit 1
fi
if [ "$(git -C "$project_path" rev-list --count HEAD)" -ne 1 ]
then
  printf 'live Codex created an unexpected commit\n' >&2
  exit 1
fi

"$binary" run logs "$run_id" --workspace canary --tail 200 --socket "$socket_path" --output json >"$live_root/logs.json"
if grep -Eq 'cf1\.run_[0-9a-f]{32}\.' "$live_root/logs.json"
then
  printf 'live Codex logs exposed a scoped capability token\n' >&2
  exit 1
fi
"$binary" daemon stop --socket "$socket_path" --output json >"$live_root/stop.json"
wait "$daemon_pid"
daemon_pid=''
printf 'Installed Codex canary: PASS\n'
