#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-sources.XXXXXX")
binary="$temp_dir/crewfold"
data_dir="$temp_dir/data"
socket_path="$temp_dir/crewfold.sock"
fixture_root="$temp_dir/git-fixture"
daemon_log="$temp_dir/daemon.log"
daemon_pid=""

cleanup() {
  if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    kill "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  if [ -d "$temp_dir" ]
  then
    find "$temp_dir" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >"$temp_dir/fixture-paths.txt"

fixture_digest() {
  find "$fixture_root" -type f -print | LC_ALL=C sort | while IFS= read -r path
  do
    sha256sum "$path"
  done | sha256sum | sed 's/ .*//'
}

start_daemon() {
  "$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" 2>>"$daemon_log" &
  daemon_pid=$!
  attempts=0
  while ! "$binary" status --socket "$socket_path" --output json >"$temp_dir/status.json" 2>/dev/null
  do
    if ! kill -0 "$daemon_pid" 2>/dev/null
    then
      wait "$daemon_pid" || true
      sed -n '1,200p' "$daemon_log" >&2
      exit 1
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 200 ]
    then
      printf 'timed out waiting for daemon readiness\n' >&2
      exit 1
    fi
    sleep 0.01
  done
}

stop_daemon() {
  "$binary" daemon stop --socket "$socket_path" --output json >"$temp_dir/stop.json"
  wait "$daemon_pid"
  daemon_pid=""
}

before_registration=$(fixture_digest)
start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key initialize-personal --output json >"$temp_dir/workspace.json"

"$binary" project add world-engine --workspace personal --repo "$fixture_root/world-engine" --mode exclusive --socket "$socket_path" --idempotency-key project-world-engine --output json >"$temp_dir/project.json"
"$binary" checkout add world-engine "$fixture_root/world-engine-2" --workspace personal --mode claimed --socket "$socket_path" --idempotency-key adjacent-two --output json >"$temp_dir/adjacent-two.json"
"$binary" checkout add world-engine "$fixture_root/world-engine-5" --workspace personal --mode exclusive --socket "$socket_path" --idempotency-key adjacent-five --output json >"$temp_dir/adjacent-five.json"
"$binary" checkout add world-engine "$fixture_root/world-engine-linked" --workspace personal --mode exclusive --socket "$socket_path" --idempotency-key linked --output json >"$temp_dir/linked.json"

after_registration=$(fixture_digest)
if [ "$before_registration" != "$after_registration" ]
then
  printf 'registration changed fixture content: %s -> %s\n' "$before_registration" "$after_registration" >&2
  exit 1
fi

repository_id=$(sed -n 's/.*"repository":{"id":"\([^"]*\)".*/\1/p' "$temp_dir/project.json")
for result in adjacent-two adjacent-five linked
do
  result_repository=$(sed -n 's/.*"repository":{"id":"\([^"]*\)".*/\1/p' "$temp_dir/$result.json")
  if [ -z "$repository_id" ] || [ "$result_repository" != "$repository_id" ]
  then
    printf '%s repository %s differs from %s\n' "$result" "$result_repository" "$repository_id" >&2
    exit 1
  fi
done
grep -Fq '"checkout_kind":"standalone"' "$temp_dir/adjacent-two.json"
grep -Fq '"checkout_kind":"linked_worktree"' "$temp_dir/linked.json"

mkdir "$temp_dir/not-a-repository"
if "$binary" project add invalid --workspace personal --repo "$temp_dir/not-a-repository" --socket "$socket_path" --idempotency-key invalid --output json >"$temp_dir/invalid.out" 2>"$temp_dir/invalid.err"
then
  printf 'non-repository registration unexpectedly succeeded\n' >&2
  exit 1
fi
grep -Fq '"code":"not_git_repository"' "$temp_dir/invalid.err"
if "$binary" checkout list invalid --workspace personal --socket "$socket_path" --output json >"$temp_dir/partial.out" 2>"$temp_dir/partial.err"
then
  printf 'failed registration left a project behind\n' >&2
  exit 1
fi
grep -Fq '"code":"project_not_found"' "$temp_dir/partial.err"

printf 'dirty\n' >"$fixture_root/world-engine-2/untracked.txt"
mv "$fixture_root/world-engine-5" "$fixture_root/world-engine-5-moved"
"$binary" project inspect world-engine --workspace personal --socket "$socket_path" --output json >"$temp_dir/inspection.json"
grep -Fq '"dirty":true' "$temp_dir/inspection.json"
grep -Fq '"availability":"unavailable"' "$temp_dir/inspection.json"
grep -Fq '"diagnostic_code":"checkout_unavailable"' "$temp_dir/inspection.json"

"$binary" checkout list world-engine --workspace personal --socket "$socket_path" --output json >"$temp_dir/list-before-restart.json"
checkout_count=$(grep -o '"id":"co_[0-9a-f]*"' "$temp_dir/list-before-restart.json" | wc -l)
if [ "$checkout_count" -ne 4 ]
then
  printf 'checkout count = %s, want 4\n' "$checkout_count" >&2
  exit 1
fi

stop_daemon
start_daemon
"$binary" checkout list world-engine --workspace personal --socket "$socket_path" --output json >"$temp_dir/list-after-restart.json"
cmp "$temp_dir/list-before-restart.json" "$temp_dir/list-after-restart.json"
stop_daemon

printf 'Projects and checkouts acceptance: PASS\n'
