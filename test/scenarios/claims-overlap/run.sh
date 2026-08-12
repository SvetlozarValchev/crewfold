#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-claims-overlap.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
fixture_root="$scenario_root/git-fixture"
daemon_log="$scenario_root/daemon.log"
daemon_pid=""

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'claims and overlap acceptance failed; collected diagnostics follow\n' >&2
    for diagnostic in "$daemon_log" "$scenario_root/first-claim.json" "$scenario_root/second-claim.json" "$scenario_root/overlaps.json" "$scenario_root/denied.json" "$scenario_root/drifts.json" "$scenario_root/claims-after-drift.json" "$scenario_root/events.json"
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
  if [ -d "$scenario_root" ]
  then
    find "$scenario_root" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >/dev/null

start_daemon() {
  "$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" 2>>"$daemon_log" &
  daemon_pid=$!
  attempts=0
  while ! "$binary" status --socket "$socket_path" --output json >"$scenario_root/status.json" 2>/dev/null
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

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key claim-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add demo --workspace personal --repo "$fixture_root/world-engine" --mode exclusive --socket "$socket_path" --idempotency-key claim-project --output json >"$scenario_root/project.json"
"$binary" checkout add demo "$fixture_root/world-engine-2" --workspace personal --mode exclusive --socket "$socket_path" --idempotency-key claim-adjacent --output json >"$scenario_root/checkout.json"
"$binary" checkout add demo "$fixture_root/world-engine-5" --workspace personal --mode shared --socket "$socket_path" --idempotency-key claim-shared --output json >"$scenario_root/shared-checkout.json"

first_checkout=$(extract_id co "$scenario_root/project.json")
second_checkout=$(extract_id co "$scenario_root/checkout.json")
shared_checkout=$(extract_id co "$scenario_root/shared-checkout.json")

"$binary" task create --workspace personal --project demo --title "Own contact package" --socket "$socket_path" --idempotency-key claim-first-task --output json >"$scenario_root/first-task.json"
"$binary" task create --workspace personal --project demo --title "Change contact cache" --socket "$socket_path" --idempotency-key claim-second-task --output json >"$scenario_root/second-task.json"
"$binary" task create --workspace personal --project demo --title "Duplicate contact change" --socket "$socket_path" --idempotency-key claim-denied-task --output json >"$scenario_root/denied-task.json"
"$binary" task create --workspace personal --project demo --title "Document shared checkout" --socket "$socket_path" --idempotency-key claim-shared-task --output json >"$scenario_root/shared-task.json"
first_task=$(extract_id task "$scenario_root/first-task.json")
second_task=$(extract_id task "$scenario_root/second-task.json")
denied_task=$(extract_id task "$scenario_root/denied-task.json")
shared_task=$(extract_id task "$scenario_root/shared-task.json")

"$binary" claim add "$first_task" --workspace personal --project demo --checkout "$first_checkout" --write 'src/contact/**' --mode exclusive --policy notify --lease 1h --socket "$socket_path" --idempotency-key claim-first --output json >"$scenario_root/first-claim.json"
"$binary" claim add "$second_task" --workspace personal --project demo --checkout "$second_checkout" --write 'src/contact/cache.go' --mode exclusive --policy notify --lease 1h --socket "$socket_path" --idempotency-key claim-second --output json >"$scenario_root/second-claim.json"

grep -Fq '"severity":"critical"' "$scenario_root/second-claim.json"
grep -Fq '"policy_response":"notify"' "$scenario_root/second-claim.json"
grep -Fq '"witness":"src/contact/cache.go"' "$scenario_root/second-claim.json"
overlap_id=$(extract_id overlap "$scenario_root/second-claim.json")
if [ -z "$overlap_id" ]
then
  printf 'overlap ID missing from second claim\n' >&2
  exit 1
fi
"$binary" overlap list --workspace personal --project demo --status open --socket "$socket_path" --output json >"$scenario_root/overlaps.json"
"$binary" overlap inspect "$overlap_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/overlap.json"
grep -Fq '"concrete shared scope witness: src/contact/cache.go"' "$scenario_root/overlap.json"

if "$binary" claim add "$denied_task" --workspace personal --project demo --checkout "$first_checkout" --write 'src/contact/*.go' --mode shared --policy deny_new --lease 1h --socket "$socket_path" --idempotency-key claim-denied --output json >"$scenario_root/denied-output.json" 2>"$scenario_root/denied.json"
then
  printf 'deny_new policy accepted a conflicting claim\n' >&2
  exit 1
fi
grep -Fq '"code":"claim_conflict"' "$scenario_root/denied.json"
"$binary" claim list --workspace personal --project demo --status active --socket "$socket_path" --output json >"$scenario_root/claims-before-drift.json"
active_count=$(grep -o '"id":"claim_' "$scenario_root/claims-before-drift.json" | wc -l)
if [ "$active_count" -ne 2 ]
then
  printf 'active claim count after deny_new=%s, want 2\n' "$active_count" >&2
  exit 1
fi

"$binary" claim add "$shared_task" --workspace personal --project demo --checkout "$shared_checkout" --write 'docs/**' --mode advisory --policy notify --lease 1h --socket "$socket_path" --idempotency-key claim-shared-warning --output json >"$scenario_root/shared-claim.json"
grep -Fq 'claims coordinate intent but do not provide filesystem isolation' "$scenario_root/shared-claim.json"

"$binary" overlap scan --workspace personal --project demo --socket "$socket_path" --output json >"$scenario_root/scan-before-stop.json"
stop_daemon
printf 'unobserved change\n' >"$fixture_root/world-engine/outside-contact.txt"
start_daemon

attempts=0
while :
do
  "$binary" drift list --workspace personal --status open --socket "$socket_path" --output json >"$scenario_root/drifts.json"
  if grep -Fq '"path":"outside-contact.txt"' "$scenario_root/drifts.json"
  then
    break
  fi
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 300 ]
  then
    printf 'timed out waiting for restart drift observation\n' >&2
    exit 1
  fi
  sleep 0.01
done
grep -Fq '"observation_gap":true' "$scenario_root/drifts.json"
"$binary" claim list --workspace personal --project demo --status active --socket "$socket_path" --output json >"$scenario_root/claims-after-drift.json"
grep -Fq '"target":"src/contact/**"' "$scenario_root/claims-after-drift.json"
if grep -Fq '"target":"outside-contact.txt"' "$scenario_root/claims-after-drift.json"
then
  printf 'drift rewrote a declared claim scope\n' >&2
  exit 1
fi

"$binary" events list --after 0 --limit 400 --socket "$socket_path" --output json >"$scenario_root/events.json"
grep -Fq '"type":"claim.added"' "$scenario_root/events.json"
grep -Fq '"type":"overlap.opened"' "$scenario_root/events.json"
grep -Fq '"type":"claim.drift_opened"' "$scenario_root/events.json"

stop_daemon
printf 'Claims, overlap, and drift acceptance: PASS\n'
