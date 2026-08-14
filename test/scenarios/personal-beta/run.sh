#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-personal-beta.XXXXXX")
binary="$scenario_root/crewfold"
source_data="$scenario_root/source-data"
source_socket="$scenario_root/source.sock"
source_log="$scenario_root/source-daemon.log"
restored_data="$scenario_root/restored-data"
restored_socket="$scenario_root/restored.sock"
restored_log="$scenario_root/restored-daemon.log"
bundle="$scenario_root/backup"
clean_home="$scenario_root/home"
clean_config="$scenario_root/config"
clean_data="$scenario_root/xdg-data"
clean_cache="$scenario_root/cache"
private_tmp="$scenario_root/tmp"
empty_path="$scenario_root/empty-path"
daemon_pid=""

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'personal beta acceptance failed; diagnostics follow\n' >&2
    for artifact in \
      source-daemon.log pending-daemon.stderr backup-create.json \
      backup-verify-offline.json backup-restore.json backup-activate.json \
      restored-doctor.json repair-inspect.json personal-load.json
    do
      if [ -s "$scenario_root/$artifact" ]
      then
        printf '\n%s:\n' "$artifact" >&2
        sed -n '1,160p' "$scenario_root/$artifact" >&2
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

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

assert_contains() {
  pattern=$1
  path=$2
  if ! grep -Fq "$pattern" "$path"
  then
    printf 'missing %s in %s\n' "$pattern" "$path" >&2
    exit 1
  fi
}

json_string() {
  key=$1
  path=$2
  sed -n 's/.*"'"$key"'":"\([^"]*\)".*/\1/p' "$path" | sed -n '1p'
}

json_number() {
  key=$1
  path=$2
  sed -n 's/.*"'"$key"'":\([0-9][0-9]*\).*/\1/p' "$path" | sed -n '1p'
}

assert_equal() {
  actual=$1
  expected=$2
  description=$3
  if [ -z "$actual" ] || [ "$actual" != "$expected" ]
  then
    printf '%s = %s, want %s\n' "$description" "$actual" "$expected" >&2
    exit 1
  fi
}

run_crewfold() {
  /usr/bin/env -i \
    HOME="$clean_home" \
    XDG_CONFIG_HOME="$clean_config" \
    XDG_DATA_HOME="$clean_data" \
    XDG_CACHE_HOME="$clean_cache" \
    TMPDIR="$private_tmp" \
    PATH="$empty_path" \
    TZ=UTC \
    NO_COLOR=1 \
    "$binary" "$@"
}

start_daemon() {
  daemon_data=$1
  daemon_socket=$2
  daemon_log=$3
  daemon_stdout=$4
  daemon_status=$5

  /usr/bin/env -i \
    HOME="$clean_home" \
    XDG_CONFIG_HOME="$clean_config" \
    XDG_DATA_HOME="$clean_data" \
    XDG_CACHE_HOME="$clean_cache" \
    TMPDIR="$private_tmp" \
    PATH="$empty_path" \
    TZ=UTC \
    NO_COLOR=1 \
    "$binary" daemon run --data-dir "$daemon_data" --socket "$daemon_socket" \
      >"$daemon_stdout" 2>>"$daemon_log" &
  daemon_pid=$!

  attempts=0
  while ! run_crewfold status --socket "$daemon_socket" --output json >"$daemon_status" 2>/dev/null
  do
    if ! kill -0 "$daemon_pid" 2>/dev/null
    then
      wait "$daemon_pid" || true
      daemon_pid=""
      printf 'daemon exited before readiness\n' >&2
      sed -n '1,200p' "$daemon_log" >&2
      exit 1
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 400 ]
    then
      fail 'timed out waiting for daemon readiness'
    fi
    sleep 0.01
  done
}

stop_daemon() {
  daemon_socket=$1
  stop_output=$2
  run_crewfold daemon stop --socket "$daemon_socket" --output json >"$stop_output"
  wait "$daemon_pid"
  daemon_pid=""
  if [ -e "$daemon_socket" ]
  then
    fail "daemon socket remained after graceful stop: $daemon_socket"
  fi
}

assert_pending_inert() {
  for relative in node.id node.key capabilities runtime check-runtime
  do
    if [ -e "$restored_data/$relative" ] || [ -L "$restored_data/$relative" ]
    then
      fail "pending restore unexpectedly created $relative"
    fi
  done
}

assert_empty_private_directory() {
  path=$1
  mode=$(stat -c '%a' "$path")
  assert_equal "$mode" 700 "mode for $path"
  if [ -n "$(find "$path" -mindepth 1 -print -quit)" ]
  then
    fail "operational directory is not empty: $path"
  fi
}

mkdir -m 0700 "$clean_home" "$clean_config" "$clean_data" "$clean_cache" \
  "$private_tmp" "$empty_path"

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -trimpath -o "$binary" "$repo_root/cmd/crewfold"

start_daemon "$source_data" "$source_socket" "$source_log" \
  "$scenario_root/source-daemon.stdout" "$scenario_root/source-status.json"

run_crewfold workspace init personal \
  --socket "$source_socket" \
  --idempotency-key personal-beta-workspace \
  --output json >"$scenario_root/workspace-init.json"
assert_contains '"event_sequence":1' "$scenario_root/workspace-init.json"
run_crewfold workspace show personal --socket "$source_socket" --output json \
  >"$scenario_root/workspace-before.json"

run_crewfold doctor --full --socket "$source_socket" --output json \
  >"$scenario_root/source-doctor.json"
assert_contains '"schema":"urn:crewfold:schema:local-api:full-doctor-result:v1"' \
  "$scenario_root/source-doctor.json"
assert_contains '"status":"ok"' "$scenario_root/source-doctor.json"
assert_equal "$(json_number event_sequence "$scenario_root/source-doctor.json")" 1 \
  'source full-doctor event sequence'
assert_contains '"code":"runtime_bindings","status":"ok"' "$scenario_root/source-doctor.json"
assert_contains '"code":"durable_queues","status":"ok"' "$scenario_root/source-doctor.json"

run_crewfold backup create \
  --socket "$source_socket" \
  --to "$bundle" \
  --idempotency-key personal-beta-cut \
  --output json >"$scenario_root/backup-create.json"
assert_contains '"schema":"urn:crewfold:schema:local-api:backup-create-result:v1"' \
  "$scenario_root/backup-create.json"
backup_id=$(json_string id "$scenario_root/backup-create.json")
backup_cursor=$(json_number event_sequence "$scenario_root/backup-create.json")
backup_logical=$(json_string logical_state_sha256 "$scenario_root/backup-create.json")
backup_manifest=$(json_string manifest_sha256 "$scenario_root/backup-create.json")
assert_equal "$backup_cursor" 1 'backup event sequence'
case "$backup_id" in
  backup_[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]) ;;
  *) fail "backup ID is not canonical: $backup_id" ;;
esac
if ! printf '%s\n' "$backup_logical" | grep -Eq '^[0-9a-f]{64}$' ||
  ! printf '%s\n' "$backup_manifest" | grep -Eq '^[0-9a-f]{64}$'
then
  fail 'backup did not return canonical logical and manifest SHA-256 values'
fi
assert_contains '"artifact_count":0' "$scenario_root/backup-create.json"

assert_equal "$(stat -c '%a' "$bundle")" 700 'backup directory mode'
assert_equal "$(find "$bundle" -type f | wc -l | tr -d ' ')" 3 'backup file count'
for relative in manifest.json manifest.sha256 crewfold.db
do
  if [ ! -f "$bundle/$relative" ] || [ -L "$bundle/$relative" ]
  then
    fail "backup entry is missing or unsafe: $relative"
  fi
  assert_equal "$(stat -c '%a' "$bundle/$relative")" 600 "backup mode for $relative"
done
for relative in node.id node.key capabilities runtime check-runtime crewfold.db-wal crewfold.db-shm
do
  if [ -e "$bundle/$relative" ] || [ -L "$bundle/$relative" ]
  then
    fail "backup leaked operational state: $relative"
  fi
done

# A later source write proves the restored event cursor is the captured cut,
# not whatever state the source reached after backup publication.
run_crewfold workspace init post-cut \
  --socket "$source_socket" \
  --idempotency-key personal-beta-post-cut \
  --output json >"$scenario_root/post-cut-workspace.json"
assert_contains '"event_sequence":2' "$scenario_root/post-cut-workspace.json"

run_crewfold backup verify "$bundle" --output json >"$scenario_root/backup-verify-live.json"
assert_contains '"schema":"urn:crewfold:schema:cli:backup-verify-response:v1"' \
  "$scenario_root/backup-verify-live.json"
assert_contains '"status":"ok"' "$scenario_root/backup-verify-live.json"
assert_equal "$(json_string id "$scenario_root/backup-verify-live.json")" "$backup_id" \
  'verified backup ID'
assert_equal "$(json_number event_sequence "$scenario_root/backup-verify-live.json")" "$backup_cursor" \
  'verified backup event sequence'
assert_equal "$(json_string logical_state_sha256 "$scenario_root/backup-verify-live.json")" "$backup_logical" \
  'verified backup logical SHA-256'

run_crewfold backup restore "$bundle" --to "$restored_data" --output json \
  >"$scenario_root/backup-restore.json"
assert_contains '"schema":"urn:crewfold:schema:cli:backup-restore-response:v1"' \
  "$scenario_root/backup-restore.json"
assert_contains '"status":"pending_activation"' "$scenario_root/backup-restore.json"
assert_contains '"pending_activation":true' "$scenario_root/backup-restore.json"
assert_equal "$(json_string backup_id "$scenario_root/backup-restore.json")" "$backup_id" \
  'restored backup ID'
assert_equal "$(json_number event_sequence "$scenario_root/backup-restore.json")" "$backup_cursor" \
  'pending restore event sequence'
assert_equal "$(json_string logical_sha256 "$scenario_root/backup-restore.json")" "$backup_logical" \
  'pending restore logical SHA-256'
assert_pending_inert

set +e
run_crewfold daemon run --data-dir "$restored_data" --socket "$restored_socket" --output json \
  >"$scenario_root/pending-daemon.stdout" 2>"$scenario_root/pending-daemon.stderr"
pending_exit=$?
set -e
assert_equal "$pending_exit" 1 'pending daemon exit code'
if [ -s "$scenario_root/pending-daemon.stdout" ]
then
  fail 'pending daemon refusal unexpectedly wrote to stdout'
fi
assert_contains '"code":"restore_not_activated"' "$scenario_root/pending-daemon.stderr"
if [ -e "$restored_socket" ] || [ -L "$restored_socket" ]
then
  fail 'pending restore created a listener socket'
fi
assert_pending_inert

# The source is still live at its post-cut cursor while the restored target is
# inert. Only after it is stopped do we make the source-retirement assertion.
run_crewfold events list --workspace personal --after 0 --limit 1000 \
  --socket "$source_socket" --output json >"$scenario_root/source-events-post-cut.json"
assert_equal "$(json_number high_water "$scenario_root/source-events-post-cut.json")" 2 \
  'live source post-cut high water'
assert_equal "$(json_number total "$scenario_root/source-events-post-cut.json")" 1 \
  'live source personal event count'
run_crewfold events list --workspace post-cut --after 0 --limit 1000 \
  --socket "$source_socket" --output json >"$scenario_root/source-post-cut-events.json"
assert_equal "$(json_number high_water "$scenario_root/source-post-cut-events.json")" 2 \
  'live source global post-cut high water'
assert_equal "$(json_number total "$scenario_root/source-post-cut-events.json")" 1 \
  'live source post-cut workspace event count'
assert_contains '"sequence":2' "$scenario_root/source-post-cut-events.json"

source_node_sha=$(sha256sum "$source_data/node.id" | sed 's/ .*//')
stop_daemon "$source_socket" "$scenario_root/source-stop.json"
find "$source_data" -depth -delete

# Offline verification remains path-based after the source daemon and database
# no longer exist.
run_crewfold backup verify "$bundle" --output json >"$scenario_root/backup-verify-offline.json"
cmp "$scenario_root/backup-verify-live.json" "$scenario_root/backup-verify-offline.json"

run_crewfold backup activate "$restored_data" --confirm-source-retired --output json \
  >"$scenario_root/backup-activate.json"
assert_contains '"schema":"urn:crewfold:schema:cli:backup-activate-response:v1"' \
  "$scenario_root/backup-activate.json"
assert_contains '"status":"activated"' "$scenario_root/backup-activate.json"
assert_contains '"source_retired":true' "$scenario_root/backup-activate.json"
assert_equal "$(json_string backup_id "$scenario_root/backup-activate.json")" "$backup_id" \
  'activated backup ID'
assert_equal "$(json_number event_sequence "$scenario_root/backup-activate.json")" "$backup_cursor" \
  'activated restore event sequence'
assert_equal "$(json_string logical_sha256 "$scenario_root/backup-activate.json")" "$backup_logical" \
  'activated restore logical SHA-256'
restored_node_sha=$(sha256sum "$restored_data/node.id" | sed 's/ .*//')
if [ "$restored_node_sha" = "$source_node_sha" ]
then
  fail 'activation reused the source node identity'
fi
assert_equal "$(stat -c '%a' "$restored_data/node.id")" 600 'restored node identity mode'
assert_equal "$(stat -c '%s' "$restored_data/node.id")" 33 'restored node identity size'
assert_equal "$(stat -c '%a' "$restored_data/node.key")" 600 'restored node key mode'
assert_equal "$(stat -c '%s' "$restored_data/node.key")" 32 'restored node key size'
for relative in capabilities runtime check-runtime
do
  assert_empty_private_directory "$restored_data/$relative"
done

start_daemon "$restored_data" "$restored_socket" "$restored_log" \
  "$scenario_root/restored-daemon.stdout" "$scenario_root/restored-status.json"
run_crewfold doctor --full --socket "$restored_socket" --output json \
  >"$scenario_root/restored-doctor.json"
assert_contains '"schema":"urn:crewfold:schema:local-api:full-doctor-result:v1"' \
  "$scenario_root/restored-doctor.json"
assert_contains '"status":"ok"' "$scenario_root/restored-doctor.json"
assert_contains '"code":"runtime_bindings","status":"ok"' "$scenario_root/restored-doctor.json"
assert_contains '"code":"restore_activation","status":"ok"' "$scenario_root/restored-doctor.json"
assert_equal "$(json_number event_sequence "$scenario_root/restored-doctor.json")" "$backup_cursor" \
  'restored full-doctor event sequence'

run_crewfold workspace show personal --socket "$restored_socket" --output json \
  >"$scenario_root/workspace-after.json"
cmp "$scenario_root/workspace-before.json" "$scenario_root/workspace-after.json"
run_crewfold events list --workspace personal --after 0 --limit 1000 \
  --socket "$restored_socket" --output json >"$scenario_root/restored-events.json"
assert_equal "$(json_number high_water "$scenario_root/restored-events.json")" "$backup_cursor" \
  'restored event high water'
assert_equal "$(json_number total "$scenario_root/restored-events.json")" 1 \
  'restored event count'
assert_contains '"type":"workspace.created"' "$scenario_root/restored-events.json"

set +e
run_crewfold workspace show post-cut --socket "$restored_socket" --output json \
  >"$scenario_root/post-cut-restored.stdout" 2>"$scenario_root/post-cut-restored.stderr"
post_cut_exit=$?
set -e
assert_equal "$post_cut_exit" 1 'post-cut workspace lookup exit code'
assert_contains '"code":"workspace_not_found"' "$scenario_root/post-cut-restored.stderr"

stop_daemon "$restored_socket" "$scenario_root/restored-stop.json"
run_crewfold repair inspect "$restored_data" --output json >"$scenario_root/repair-inspect.json"
assert_contains '"schema":"urn:crewfold:schema:cli:repair-inspect-response:v1"' \
  "$scenario_root/repair-inspect.json"
assert_contains '"status":"ok"' "$scenario_root/repair-inspect.json"
assert_equal "$(json_number event_sequence "$scenario_root/repair-inspect.json")" "$backup_cursor" \
  'offline repair event sequence'
assert_equal "$(json_string logical_sha256 "$scenario_root/repair-inspect.json")" "$backup_logical" \
  'offline repair logical SHA-256'

# The scale profile is deliberately last. It owns and removes its fixture and
# cannot resolve any provider executable because the acceptance PATH is empty.
run_crewfold test load --profile personal-100 --output json \
  >"$scenario_root/personal-load.json"
assert_contains '"schema":"urn:crewfold:schema:cli:personal-load-report:v1"' \
  "$scenario_root/personal-load.json"
assert_contains '"profile":"personal-100"' "$scenario_root/personal-load.json"
assert_contains '"status":"ok"' "$scenario_root/personal-load.json"
assert_contains '"counts":{"workspaces":1,"projects":10,"agents":100,"objectives":10,"tasks":1000,"known_events":100000,"noisy_project_events":80000}' \
  "$scenario_root/personal-load.json"
if grep -Fq '"passed":false' "$scenario_root/personal-load.json"
then
  fail 'personal-100 reported a failed fixed assertion'
fi
assert_contains '"name":"quiet_project_briefing_fairness","passed":true' \
  "$scenario_root/personal-load.json"
if ! json_string logical_sha256 "$scenario_root/personal-load.json" | grep -Eq '^[0-9a-f]{64}$'
then
  fail 'personal-100 did not return a canonical logical SHA-256'
fi
if [ -n "$(find "$private_tmp" -mindepth 1 -print -quit)" ]
then
  fail 'personal-100 or repair inspection leaked a temporary fixture'
fi
for path in "$clean_home" "$clean_config" "$clean_data" "$clean_cache"
do
  if [ -n "$(find "$path" -mindepth 1 -print -quit)" ]
  then
    fail "provider-free acceptance wrote outside its owned data roots: $path"
  fi
done

printf 'Provider-free personal beta recovery and load acceptance: PASS\n'
