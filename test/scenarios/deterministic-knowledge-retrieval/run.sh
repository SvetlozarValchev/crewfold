#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-knowledge-retrieval.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
fixture_root="$scenario_root/git-fixture"
archive_checkout="$scenario_root/archive-repository"
daemon_log="$scenario_root/daemon.log"
daemon_pid=''

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'Deterministic knowledge retrieval acceptance failed; diagnostics follow\n' >&2
    for diagnostic in \
      "$daemon_log" \
      "$scenario_root/task-search.json" \
      "$scenario_root/project-search.json" \
      "$scenario_root/mismatch-search-error.json" \
      "$scenario_root/mismatch-index.json" \
      "$scenario_root/mismatch-doctor.json" \
      "$scenario_root/degraded-search-error.json" \
      "$scenario_root/degraded-index.json" \
      "$scenario_root/degraded-doctor.json" \
      "$scenario_root/rebuild.json" \
      "$scenario_root/repaired-search.json" \
      "$scenario_root/restarted-search.json"
    do
      if [ -f "$diagnostic" ]
      then
        printf '%s\n' "$diagnostic" >&2
        if [ "$diagnostic" = "$daemon_log" ]
        then
          tail -30 "$diagnostic" >&2
        else
          sed -n '1,240p' "$diagnostic" >&2
        fi
      fi
    done
  fi
  if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    kill "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  if [ "${CREWFOLD_KEEP_SCENARIO:-}" = 1 ]
  then
    printf 'Preserved diagnostics: %s\n' "$scenario_root" >&2
  elif [ -d "$scenario_root" ]
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
  path=$1
  expected=$2
  message=$3
  grep -Fq "$expected" "$path" || fail "$message"
}

assert_absent() {
  path=$1
  unexpected=$2
  message=$3
  if grep -Fq "$unexpected" "$path"
  then
    fail "$message"
  fi
}

assert_before() {
  path=$1
  first=$2
  second=$3
  message=$4
  awk -v first="$first" -v second="$second" '
    index($0, first) > 0 && index($0, second) > 0 && index($0, first) < index($0, second) { found = 1 }
    END { exit(found ? 0 : 1) }
  ' "$path" || fail "$message"
}

extract_match_order() {
  awk '
    {
      rest = $0
      marker = "\"revision\":{\"id\":\""
      while ((start = index(rest, marker)) > 0) {
        rest = substr(rest, start + length(marker))
        stop = index(rest, "\"")
        print substr(rest, 1, stop - 1)
        rest = substr(rest, stop + 1)
      }
    }
  ' "$1"
}

extract_id() {
  prefix=$1
  path=$2
  sed -n "s/.*\"id\":\"\(${prefix}_[0-9a-f]*\)\".*/\1/p" "$path" | sed -n '1p'
}

start_daemon() {
  "$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" 2>>"$daemon_log" &
  daemon_pid=$!
  attempts=0
  until "$binary" status --socket "$socket_path" --output json >"$scenario_root/status.json" 2>/dev/null
  do
    if ! kill -0 "$daemon_pid" 2>/dev/null
    then
      wait "$daemon_pid" || true
      fail 'daemon exited before readiness'
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 300 ]
    then
      fail 'timed out waiting for daemon readiness'
    fi
    sleep 0.01
  done
}

stop_daemon() {
  name=$1
  "$binary" daemon stop --socket "$socket_path" --output json >"$scenario_root/$name-stop.json"
  wait "$daemon_pid"
  daemon_pid=''
  [ ! -e "$socket_path" ] || fail "socket remained after $name daemon stop"
}

create_task() {
  output=$1
  project=$2
  title=$3
  key=$4
  "$binary" task create --workspace personal --project "$project" --title "$title" \
    --socket "$socket_path" --idempotency-key "$key" --output json >"$output"
}

propose_finding() {
  output=$1
  source_task=$2
  title=$3
  body=$4
  key=$5
  shift 5
  markdown="$scenario_root/$key.md"
  {
    printf '# %s\n\n' "$title"
    printf '%s\n' "$body"
  } >"$markdown"
  "$binary" knowledge propose "$markdown" --workspace personal --type finding \
    --from-task "$source_task" --socket "$socket_path" --idempotency-key "$key" \
    "$@" --output json >"$output"
}

accept_finding() {
  output=$1
  revision=$2
  key=$3
  "$binary" knowledge accept "$revision" --workspace personal \
    --expected-state-revision 1 --note 'Accepted for deterministic retrieval acceptance.' \
    --socket "$socket_path" --idempotency-key "$key" --output json >"$output"
}

propose_and_accept() {
  name=$1
  source_task=$2
  title=$3
  body=$4
  shift 4
  propose_finding "$scenario_root/$name-proposed.json" "$source_task" "$title" "$body" \
    "retrieval-$name-proposal" "$@"
  revision=$(extract_id krev "$scenario_root/$name-proposed.json")
  [ -n "$revision" ] || fail "knowledge revision ID is missing for $name"
  accept_finding "$scenario_root/$name-accepted.json" "$revision" "retrieval-$name-accept"
  printf '%s\n' "$revision"
}

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >/dev/null

git init --quiet --initial-branch=main "$archive_checkout"
git -C "$archive_checkout" config user.name 'Crewfold Retrieval Fixture'
git -C "$archive_checkout" config user.email 'retrieval@crewfold.invalid'
printf 'independent archive fixture\n' >"$archive_checkout/README.md"
git -C "$archive_checkout" add README.md
GIT_AUTHOR_DATE='2026-01-02T00:00:00Z' \
GIT_COMMITTER_DATE='2026-01-02T00:00:00Z' \
  git -C "$archive_checkout" commit --quiet -m 'archive fixture root'

start_daemon
"$binary" workspace init personal --socket "$socket_path" --idempotency-key retrieval-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add contacts --workspace personal --repo "$fixture_root/world-engine" --mode exclusive \
  --socket "$socket_path" --idempotency-key retrieval-project --output json >"$scenario_root/project.json"
"$binary" project add archive --workspace personal --repo "$archive_checkout" --mode exclusive \
  --socket "$socket_path" --idempotency-key retrieval-archive-project --output json >"$scenario_root/archive-project.json"
"$binary" agent create retrieval-reader --workspace personal --role reviewer --provider fake --runtime fake \
  --socket "$socket_path" --idempotency-key retrieval-agent --output json >"$scenario_root/agent.json"

create_task "$scenario_root/dependency-task.json" contacts 'Establish ordering dependency' retrieval-dependency-task
dependency_task=$(extract_id task "$scenario_root/dependency-task.json")
create_task "$scenario_root/search-task.json" contacts 'Consume contact ordering knowledge' retrieval-search-task
search_task=$(extract_id task "$scenario_root/search-task.json")
create_task "$scenario_root/context-task.json" contacts 'Inspect exact retrieval-independent context' retrieval-context-task
context_task=$(extract_id task "$scenario_root/context-task.json")
create_task "$scenario_root/unrelated-task.json" contacts 'Maintain unrelated contact behavior' retrieval-unrelated-task
unrelated_task=$(extract_id task "$scenario_root/unrelated-task.json")
create_task "$scenario_root/wrong-scope-task.json" contacts 'Private alternate ordering experiment' retrieval-wrong-scope-task
wrong_scope_task=$(extract_id task "$scenario_root/wrong-scope-task.json")
create_task "$scenario_root/archive-task.json" archive 'Archive contact ordering text' retrieval-archive-task
archive_task=$(extract_id task "$scenario_root/archive-task.json")
for required_id in "$dependency_task" "$search_task" "$context_task" "$unrelated_task" "$wrong_scope_task" "$archive_task"
do
  [ -n "$required_id" ] || fail 'a retrieval fixture task ID is missing'
done

"$binary" task depend "$search_task" --on "$dependency_task" --workspace personal --expected-revision 1 \
  --socket "$socket_path" --idempotency-key retrieval-dependency --output json >"$scenario_root/search-dependent.json"
"$binary" task assign "$context_task" retrieval-reader --workspace personal --expected-revision 1 --lease-seconds 600 \
  --socket "$socket_path" --idempotency-key retrieval-context-assignment --output json >"$scenario_root/context-assigned.json"

# An accepted revision with an explicit short deadline proves that expiry is a
# canonical hard filter, not a rank penalty. Five seconds leaves ample time for
# proposal/acceptance before the deliberate wait.
expiry=$(date -u -d '+5 seconds' '+%Y-%m-%dT%H:%M:%SZ')
expired_revision=$(propose_and_accept expired "$unrelated_task" \
  'Contact ordering expired duplicate' \
  'Contact ordering contact ordering contact ordering was deliberately time bounded.' \
  --confidence high --verification verified --fresh-until "$expiry")
sleep 6

exact_task_revision=$(propose_and_accept exact-task "$search_task" \
  'Contact ordering task rule' \
  'Contact ordering for this exact task remains intentionally low-confidence.' \
  --task-scope "$search_task" --confidence low --verification unverified)
task_source_revision=$(propose_and_accept task-source "$search_task" \
  'Contact ordering task provenance' \
  'Contact ordering applies project-wide but originates in the consuming task.' \
  --confidence low --verification unverified)
dependency_revision=$(propose_and_accept dependency "$dependency_task" \
  'Contact ordering dependency provenance' \
  'Contact ordering comes from a direct dependency with stronger quality metadata.' \
  --confidence high --verification verified)
high_quality_revision=$(propose_and_accept high-quality "$unrelated_task" \
  'Contact ordering quality comparison' \
  'Contact ordering quality comparison uses otherwise equal searchable content.' \
  --confidence high --verification verified)
low_quality_revision=$(propose_and_accept low-quality "$unrelated_task" \
  'Contact ordering quality comparison' \
  'Contact ordering quality comparison uses otherwise equal searchable content.' \
  --confidence low --verification verified)
verified_revision=$(propose_and_accept verified "$unrelated_task" \
  'Contact ordering verification comparison' \
  'Contact ordering verification comparison uses otherwise equal searchable content.' \
  --confidence high --verification verified)
supported_revision=$(propose_and_accept supported "$unrelated_task" \
  'Contact ordering verification comparison' \
  'Contact ordering verification comparison uses otherwise equal searchable content.' \
  --confidence high --verification supported)
title_match_revision=$(propose_and_accept title-match "$unrelated_task" \
  'Contact ordering' 'A concise independently accepted rule.' \
  --confidence high --verification verified)
body_match_revision=$(propose_and_accept body-match "$unrelated_task" \
  'Independent accepted rule' 'Contact ordering.' \
  --confidence high --verification verified)

propose_finding "$scenario_root/proposed.json" "$unrelated_task" \
  'Contact ordering proposed duplicate' \
  'Contact ordering contact ordering contact ordering remains unaccepted.' \
  retrieval-proposed --confidence high --verification verified
proposed_revision=$(extract_id krev "$scenario_root/proposed.json")

stale_revision=$(propose_and_accept stale "$unrelated_task" \
  'Contact ordering stale duplicate' \
  'Contact ordering contact ordering contact ordering has been invalidated.' \
  --confidence high --verification verified)
"$binary" knowledge mark-stale "$stale_revision" --workspace personal --expected-state-revision 2 \
  --reason 'Deliberately stale retrieval fixture.' --socket "$socket_path" \
  --idempotency-key retrieval-mark-stale --output json >"$scenario_root/stale.json"

wrong_task_revision=$(propose_and_accept wrong-task "$wrong_scope_task" \
  'Contact ordering wrong task duplicate' \
  'Contact ordering contact ordering contact ordering is private to another task.' \
  --task-scope "$wrong_scope_task" --confidence high --verification verified)
wrong_project_revision=$(propose_and_accept wrong-project "$archive_task" \
  'Contact ordering wrong project duplicate' \
  'Contact ordering contact ordering contact ordering belongs to another project.' \
  --confidence high --verification verified)

"$binary" knowledge index rebuild --workspace personal --socket "$socket_path" \
  --idempotency-key retrieval-initial-rebuild --output json >"$scenario_root/initial-rebuild.json"
assert_contains "$scenario_root/initial-rebuild.json" '"type":"knowledge_index_rebuild"' 'initial index rebuild returned the wrong result type'
assert_contains "$scenario_root/initial-rebuild.json" '"status":"ok"' 'initial index rebuild did not publish an ok generation'
"$binary" knowledge index rebuild --workspace personal --socket "$socket_path" \
  --idempotency-key retrieval-initial-rebuild --output json >"$scenario_root/initial-rebuild-replay.json"
cmp "$scenario_root/initial-rebuild.json" "$scenario_root/initial-rebuild-replay.json"
"$binary" knowledge index status --workspace personal --socket "$socket_path" --output json >"$scenario_root/initial-index.json"
assert_contains "$scenario_root/initial-index.json" '"status":"ok"' 'initial index status is not ok'
assert_contains "$scenario_root/initial-index.json" '"generation":2' 'initial explicit rebuild did not advance generation 1 to 2'

"$binary" context build "$context_task" --workspace personal --agent retrieval-reader \
  --expected-task-revision 2 --include "$task_source_revision" --socket "$socket_path" \
  --idempotency-key retrieval-context --output json >"$scenario_root/context-build.json"
context_id=$(extract_id ctx "$scenario_root/context-build.json")
[ -n "$context_id" ] || fail 'retrieval fixture context ID is missing'

# Freeze canonical/query-independent public state before search. Search must not
# append an event, govern a revision, or mutate an exact packet.
"$binary" knowledge show "$task_source_revision" --workspace personal --socket "$socket_path" --output json >"$scenario_root/show-baseline.json"
"$binary" knowledge list --workspace personal --project contacts --socket "$socket_path" --output json >"$scenario_root/list-baseline.json"
"$binary" context show "$context_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/context-baseline.json"
"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events-baseline.json"

"$binary" knowledge search 'contact ordering' --workspace personal --project contacts --task "$search_task" \
  --limit 100 --socket "$socket_path" --output json >"$scenario_root/task-search.json"
assert_contains "$scenario_root/task-search.json" '"type":"knowledge_search"' 'task search returned the wrong result type'
assert_contains "$scenario_root/task-search.json" '"normalized_query":"contact ordering"' 'search did not expose its normalized literal query'
assert_contains "$scenario_root/task-search.json" '"rank_policy":"knowledge_search_v1"' 'search omitted its versioned rank policy'
assert_contains "$scenario_root/task-search.json" '"status":"ok"' 'search did not identify its healthy index generation'
assert_contains "$scenario_root/task-search.json" '"ordinal":1' 'search omitted one-based result ordinals'

for included_revision in "$exact_task_revision" "$task_source_revision" "$dependency_revision" "$high_quality_revision" "$low_quality_revision" "$verified_revision" "$supported_revision" "$title_match_revision" "$body_match_revision"
do
  assert_contains "$scenario_root/task-search.json" "\"id\":\"$included_revision\"" "eligible revision $included_revision is absent from task search"
done
for excluded_revision in "$proposed_revision" "$stale_revision" "$expired_revision" "$wrong_task_revision" "$wrong_project_revision"
do
  assert_absent "$scenario_root/task-search.json" "$excluded_revision" "ineligible revision $excluded_revision leaked into task search"
done

assert_before "$scenario_root/task-search.json" "$exact_task_revision" "$task_source_revision" 'exact task scope did not outrank project-wide scope'
assert_before "$scenario_root/task-search.json" "$task_source_revision" "$dependency_revision" 'primary exact-task provenance did not outrank direct-dependency provenance'
assert_before "$scenario_root/task-search.json" "$high_quality_revision" "$low_quality_revision" 'high confidence did not outrank low confidence after equal earlier axes'
assert_before "$scenario_root/task-search.json" "$verified_revision" "$supported_revision" 'verified knowledge did not outrank supported knowledge after equal earlier axes'
assert_before "$scenario_root/task-search.json" "$title_match_revision" "$body_match_revision" 'title-weighted BM25 did not outrank an otherwise equal body-only match'
assert_contains "$scenario_root/task-search.json" '"scope":{"rank":0' 'search explanation omitted exact task-scope rank'
assert_contains "$scenario_root/task-search.json" '"title_weight":8' 'search explanation omitted the frozen title weight'
assert_contains "$scenario_root/task-search.json" '"body_weight":1' 'search explanation omitted the frozen body weight'
extract_match_order "$scenario_root/task-search.json" >"$scenario_root/task-search-order.txt"

"$binary" knowledge search 'contact ordering' --workspace personal --project contacts \
  --limit 100 --socket "$socket_path" --output json >"$scenario_root/project-search.json"
assert_absent "$scenario_root/project-search.json" "$exact_task_revision" 'broad project search leaked an exact-task revision'
assert_absent "$scenario_root/project-search.json" "$wrong_task_revision" 'broad project search leaked another task revision'
assert_absent "$scenario_root/project-search.json" "$wrong_project_revision" 'project search leaked another project revision'
assert_contains "$scenario_root/project-search.json" "\"id\":\"$task_source_revision\"" 'project search omitted eligible project-wide knowledge'

"$binary" knowledge show "$task_source_revision" --workspace personal --socket "$socket_path" --output json >"$scenario_root/show-after-search.json"
"$binary" knowledge list --workspace personal --project contacts --socket "$socket_path" --output json >"$scenario_root/list-after-search.json"
"$binary" context show "$context_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/context-after-search.json"
"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events-after-search.json"
cmp "$scenario_root/show-baseline.json" "$scenario_root/show-after-search.json"
cmp "$scenario_root/list-baseline.json" "$scenario_root/list-after-search.json"
cmp "$scenario_root/context-baseline.json" "$scenario_root/context-after-search.json"
cmp "$scenario_root/events-baseline.json" "$scenario_root/events-after-search.json"

stop_daemon before-content-mismatch

# Build a disposable failure injector with the same pinned CGO-free driver. It
# can invalidate one derived row or remove only the derived FTS virtual table from
# a stopped database.
tamper_source="$scenario_root/damage-derived-index.go"
sed \
  -e 's|@DATABASE_DRIVER@|github.com/ncruces/go-sqlite3/driver|' \
  -e 's|@FTS_EXTENSION@|github.com/ncruces/go-sqlite3/ext/fts5|' \
  >"$tamper_source" <<'TAMPER'
package main

import (
	"fmt"
	"os"

	"@DATABASE_DRIVER@"
	"@FTS_EXTENSION@"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: damage-derived-index <database> delete-row|drop-table")
		os.Exit(2)
	}
	database, err := driver.Open(os.Args[1], fts5.Register)
	if err != nil {
		panic(err)
	}
	defer database.Close()
	statement := ""
	switch os.Args[2] {
	case "delete-row":
		statement = "DELETE FROM knowledge_search WHERE rowid = (SELECT min(rowid) FROM knowledge_search)"
	case "drop-table":
		statement = "DROP TABLE knowledge_search"
	default:
		fmt.Fprintln(os.Stderr, "unknown damage mode")
		os.Exit(2)
	}
	if _, err := database.Exec(statement); err != nil {
		panic(err)
	}
}
TAMPER
GOTOOLCHAIN=local GOPROXY=off "$go_runner" run "$tamper_source" "$data_dir/crewfold.db" delete-row

start_daemon
"$binary" knowledge index status --workspace personal --socket "$socket_path" --output json >"$scenario_root/mismatch-index.json"
assert_contains "$scenario_root/mismatch-index.json" '"status":"degraded"' 'inconsistent derived index was not reported as degraded'
assert_contains "$scenario_root/mismatch-index.json" '"diagnosis":"content_mismatch"' 'derived index content mismatch diagnosis is not stable'
if "$binary" doctor --retrieval --workspace personal --socket "$socket_path" --output json >"$scenario_root/mismatch-doctor.json" 2>"$scenario_root/mismatch-doctor-error.json"
then
  fail 'retrieval doctor succeeded for inconsistent index content'
fi
assert_contains "$scenario_root/mismatch-doctor.json" '"diagnosis":"content_mismatch"' 'retrieval doctor omitted content mismatch diagnosis'
if "$binary" knowledge search 'contact ordering' --workspace personal --project contacts --task "$search_task" \
  --limit 100 --socket "$socket_path" --output json >"$scenario_root/mismatch-search-output.json" 2>"$scenario_root/mismatch-search-error.json"
then
  fail 'search succeeded with inconsistent derived index content'
fi
assert_contains "$scenario_root/mismatch-search-error.json" '"code":"retrieval_degraded"' 'content-mismatch search did not return retrieval_degraded'

"$binary" knowledge show "$task_source_revision" --workspace personal --socket "$socket_path" --output json >"$scenario_root/show-mismatch.json"
"$binary" knowledge list --workspace personal --project contacts --socket "$socket_path" --output json >"$scenario_root/list-mismatch.json"
"$binary" context show "$context_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/context-mismatch.json"
"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events-mismatch.json"
cmp "$scenario_root/show-baseline.json" "$scenario_root/show-mismatch.json"
cmp "$scenario_root/list-baseline.json" "$scenario_root/list-mismatch.json"
cmp "$scenario_root/context-baseline.json" "$scenario_root/context-mismatch.json"
cmp "$scenario_root/events-baseline.json" "$scenario_root/events-mismatch.json"

"$binary" knowledge index rebuild --workspace personal --socket "$socket_path" \
  --idempotency-key retrieval-mismatch-repair --output json >"$scenario_root/mismatch-rebuild.json"
assert_contains "$scenario_root/mismatch-rebuild.json" '"status":"ok"' 'explicit rebuild did not repair content mismatch'
assert_contains "$scenario_root/mismatch-rebuild.json" '"generation":3' 'content repair did not publish the next index generation'
"$binary" knowledge search 'contact ordering' --workspace personal --project contacts --task "$search_task" \
  --limit 100 --socket "$socket_path" --output json >"$scenario_root/mismatch-repaired-search.json"
assert_before "$scenario_root/mismatch-repaired-search.json" "$exact_task_revision" "$task_source_revision" 'content repair changed applicability order'
extract_match_order "$scenario_root/mismatch-repaired-search.json" >"$scenario_root/mismatch-repaired-order.txt"
cmp "$scenario_root/task-search-order.txt" "$scenario_root/mismatch-repaired-order.txt"

stop_daemon before-missing-index
GOTOOLCHAIN=local GOPROXY=off "$go_runner" run "$tamper_source" "$data_dir/crewfold.db" drop-table

start_daemon
"$binary" knowledge index status --workspace personal --socket "$socket_path" --output json >"$scenario_root/degraded-index.json"
assert_contains "$scenario_root/degraded-index.json" '"status":"degraded"' 'missing derived index was not reported as degraded'
assert_contains "$scenario_root/degraded-index.json" '"diagnosis":"missing"' 'missing derived index diagnosis is not stable'
if "$binary" doctor --retrieval --workspace personal --socket "$socket_path" --output json >"$scenario_root/degraded-doctor.json" 2>"$scenario_root/degraded-doctor-error.json"
then
  fail 'retrieval doctor succeeded for a missing index'
fi
assert_contains "$scenario_root/degraded-doctor.json" '"status":"degraded"' 'retrieval doctor did not emit structured degraded status'
if "$binary" knowledge search 'contact ordering' --workspace personal --project contacts --task "$search_task" \
  --limit 100 --socket "$socket_path" --output json >"$scenario_root/degraded-search-output.json" 2>"$scenario_root/degraded-search-error.json"
then
  fail 'search succeeded with a missing derived index'
fi
assert_contains "$scenario_root/degraded-search-error.json" '"code":"retrieval_degraded"' 'missing-index search did not return retrieval_degraded'

# Exact canonical surfaces remain byte-for-byte readable while FTS is absent.
"$binary" knowledge show "$task_source_revision" --workspace personal --socket "$socket_path" --output json >"$scenario_root/show-degraded.json"
"$binary" knowledge list --workspace personal --project contacts --socket "$socket_path" --output json >"$scenario_root/list-degraded.json"
"$binary" context show "$context_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/context-degraded.json"
"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events-degraded.json"
cmp "$scenario_root/show-baseline.json" "$scenario_root/show-degraded.json"
cmp "$scenario_root/list-baseline.json" "$scenario_root/list-degraded.json"
cmp "$scenario_root/context-baseline.json" "$scenario_root/context-degraded.json"
cmp "$scenario_root/events-baseline.json" "$scenario_root/events-degraded.json"

"$binary" knowledge index rebuild --workspace personal --socket "$socket_path" \
  --idempotency-key retrieval-repair --output json >"$scenario_root/rebuild.json"
assert_contains "$scenario_root/rebuild.json" '"status":"ok"' 'explicit rebuild did not repair the missing index'
assert_contains "$scenario_root/rebuild.json" '"generation":4' 'repair did not publish the next index generation'
"$binary" knowledge search 'contact ordering' --workspace personal --project contacts --task "$search_task" \
  --limit 100 --socket "$socket_path" --output json >"$scenario_root/repaired-search.json"
assert_before "$scenario_root/repaired-search.json" "$exact_task_revision" "$task_source_revision" 'repaired index changed applicability order'
assert_before "$scenario_root/repaired-search.json" "$task_source_revision" "$dependency_revision" 'repaired index changed provenance order'
extract_match_order "$scenario_root/repaired-search.json" >"$scenario_root/repaired-search-order.txt"
cmp "$scenario_root/task-search-order.txt" "$scenario_root/repaired-search-order.txt"

"$binary" knowledge show "$task_source_revision" --workspace personal --socket "$socket_path" --output json >"$scenario_root/show-repaired.json"
"$binary" knowledge list --workspace personal --project contacts --socket "$socket_path" --output json >"$scenario_root/list-repaired.json"
"$binary" context show "$context_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/context-repaired.json"
"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$socket_path" --output json >"$scenario_root/events-repaired.json"
cmp "$scenario_root/show-baseline.json" "$scenario_root/show-repaired.json"
cmp "$scenario_root/list-baseline.json" "$scenario_root/list-repaired.json"
cmp "$scenario_root/context-baseline.json" "$scenario_root/context-repaired.json"
cmp "$scenario_root/events-baseline.json" "$scenario_root/events-repaired.json"

"$binary" knowledge index status --workspace personal --socket "$socket_path" --output json >"$scenario_root/index-before-restart.json"
stop_daemon after-repair
start_daemon
"$binary" knowledge index status --workspace personal --socket "$socket_path" --output json >"$scenario_root/index-after-restart.json"
cmp "$scenario_root/index-before-restart.json" "$scenario_root/index-after-restart.json"
"$binary" knowledge search 'contact ordering' --workspace personal --project contacts --task "$search_task" \
  --limit 100 --socket "$socket_path" --output json >"$scenario_root/restarted-search.json"
assert_before "$scenario_root/restarted-search.json" "$exact_task_revision" "$task_source_revision" 'restart changed applicability order'
assert_before "$scenario_root/restarted-search.json" "$task_source_revision" "$dependency_revision" 'restart changed provenance order'
assert_before "$scenario_root/restarted-search.json" "$high_quality_revision" "$low_quality_revision" 'restart changed quality order'
assert_absent "$scenario_root/restarted-search.json" "$wrong_task_revision" 'restart leaked wrong-task knowledge'
assert_absent "$scenario_root/restarted-search.json" "$wrong_project_revision" 'restart leaked wrong-project knowledge'
extract_match_order "$scenario_root/restarted-search.json" >"$scenario_root/restarted-search-order.txt"
cmp "$scenario_root/task-search-order.txt" "$scenario_root/restarted-search-order.txt"

stop_daemon final
printf 'Deterministic knowledge retrieval acceptance: PASS\n'
