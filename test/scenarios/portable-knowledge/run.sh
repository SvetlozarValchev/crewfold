#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-portable-knowledge.XXXXXX")
binary="$scenario_root/crewfold"
fixture_root="$scenario_root/git-fixture"
source_data="$scenario_root/source-data"
source_socket="$scenario_root/source.sock"
target_data="$scenario_root/target-data"
target_socket="$scenario_root/target.sock"
daemon_log="$scenario_root/daemon.log"
daemon_pid=''
active_data=''
active_socket=''

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'Portable project knowledge acceptance failed; diagnostics follow\n' >&2
    printf '%s\n' "$daemon_log" >&2
    if [ -f "$daemon_log" ]; then sed -n '1,300p' "$daemon_log" >&2; fi
    find "$scenario_root" -maxdepth 2 -type f \( -name '*.json' -o -name '*.err' \) -print 2>/dev/null | while IFS= read -r diagnostic
    do
      printf '%s\n' "$diagnostic" >&2
      sed -n '1,160p' "$diagnostic" >&2
    done
  fi
  if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    kill "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  if [ -d "$scenario_root" ]; then find "$scenario_root" -depth -delete; fi
}
trap cleanup EXIT HUP INT TERM

fail() { printf '%s\n' "$1" >&2; exit 1; }
assert_contains() { grep -Fq "$2" "$1" || fail "$3"; }
assert_absent() { if grep -Fq "$2" "$1"; then fail "$3"; fi; }
assert_occurrences() {
  actual=$(grep -Fo "$2" "$1" | wc -l | tr -d ' ')
  [ "$actual" = "$3" ] || fail "$4 (got $actual, wanted $3)"
}
extract_id() {
  sed -n "s/.*\"id\":\"\($1_[0-9a-f]*\)\".*/\1/p" "$2" | sed -n '1p'
}
extract_content_digest() {
  sed -n 's/.*"content_sha256":"\([0-9a-f][0-9a-f]*\)".*/\1/p' "$1" | sed -n '1p'
}
assert_error() {
  expected=$1
  output=$2
  shift 2
  if "$@" >"$output.out" 2>"$output"
  then
    fail "command unexpectedly succeeded; wanted $expected"
  fi
  assert_contains "$output" "\"code\":\"$expected\"" "command returned the wrong error; wanted $expected"
}

start_daemon() {
  active_data=$1
  active_socket=$2
  "$binary" daemon run --data-dir "$active_data" --socket "$active_socket" 2>>"$daemon_log" &
  daemon_pid=$!
  attempts=0
  until "$binary" status --socket "$active_socket" --output json >"$scenario_root/status.json" 2>/dev/null
  do
    if ! kill -0 "$daemon_pid" 2>/dev/null; then wait "$daemon_pid" || true; fail 'daemon exited before readiness'; fi
    attempts=$((attempts + 1))
    [ "$attempts" -lt 300 ] || fail 'timed out waiting for daemon readiness'
    sleep 0.01
  done
}

stop_daemon() {
  "$binary" daemon stop --socket "$active_socket" --output json >"$scenario_root/$1-stop.json"
  wait "$daemon_pid"
  daemon_pid=''
  [ ! -e "$active_socket" ] || fail 'socket remained after daemon stop'
}

create_task() {
  "$binary" task create --workspace personal --project demo --title "$2" \
    --socket "$active_socket" --idempotency-key "$3" --output json >"$1"
}

propose() {
  output=$1; source_task=$2; title=$3; body=$4; key=$5
  shift 5
  markdown="$scenario_root/$key.md"
  { printf '# %s\n\n' "$title"; printf '%s\n' "$body"; } >"$markdown"
  "$binary" knowledge propose "$markdown" --workspace personal --type decision \
    --from-task "$source_task" --confidence high --verification verified \
    --socket "$active_socket" --idempotency-key "$key" "$@" --output json >"$output"
}

accept() {
  "$binary" knowledge accept "$2" --workspace personal --expected-state-revision 1 \
    --note 'Portable fixture owner acceptance.' --socket "$active_socket" \
    --idempotency-key "$3" --output json >"$1"
}

propose_accept() {
  name=$1; source_task=$2; title=$3; body=$4
  shift 4
  propose "$scenario_root/$name-proposed.json" "$source_task" "$title" "$body" "portable-$name-propose" "$@"
  revision=$(extract_id krev "$scenario_root/$name-proposed.json")
  [ -n "$revision" ] || fail "missing revision for $name"
  accept "$scenario_root/$name-accepted.json" "$revision" "portable-$name-accept"
  printf '%s\n' "$revision"
}

report_contradiction() {
  "$binary" contradiction report "$2" "$3" --workspace personal --reason "$4" \
    --socket "$active_socket" --idempotency-key "$5" --output json >"$1"
}

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -o "$binary" "$repo_root/cmd/crewfold"
"$repo_root/test/fixtures/git/create.sh" "$fixture_root" >/dev/null

start_daemon "$source_data" "$source_socket"
"$binary" workspace init personal --socket "$active_socket" --idempotency-key portable-workspace --output json >"$scenario_root/workspace.json"
"$binary" project add demo --workspace personal --repo "$fixture_root/world-engine" --mode shared \
  --socket "$active_socket" --idempotency-key portable-project --output json >"$scenario_root/project.json"
create_task "$scenario_root/source-task.json" 'Portable primary source' portable-source-task
source_task=$(extract_id task "$scenario_root/source-task.json")
create_task "$scenario_root/support-task.json" 'Portable supporting source' portable-support-task
support_task=$(extract_id task "$scenario_root/support-task.json")
[ -n "$source_task" ] && [ -n "$support_task" ] || fail 'source task IDs are missing'

broad_revision=$(propose_accept broad "$source_task" 'Portable broad route' 'Use the portable broad route for all work.' --supporting-task "$support_task")
scoped_revision=$(propose_accept scoped "$source_task" 'Portable private route' 'Use the private route only for the exact source task.' --task-scope "$source_task")
scoped_safe_revision=$(propose_accept scoped-safe "$source_task" 'Portable scoped safe route' 'This task-only route remains independently retrievable.' --task-scope "$source_task")

propose "$scenario_root/pending.json" "$source_task" 'Portable pending decision' 'This proposal remains pending.' portable-pending
pending_revision=$(extract_id krev "$scenario_root/pending.json")
propose "$scenario_root/rejected-proposed.json" "$source_task" 'Portable rejected decision' 'This proposal is rejected.' portable-rejected
rejected_revision=$(extract_id krev "$scenario_root/rejected-proposed.json")
"$binary" knowledge reject "$rejected_revision" --workspace personal --expected-state-revision 1 \
  --note 'Rejected for portable lifecycle coverage.' --socket "$active_socket" \
  --idempotency-key portable-reject --output json >"$scenario_root/rejected.json"

stale_revision=$(propose_accept stale "$source_task" 'Portable stale decision' 'This accepted decision becomes stale.')
"$binary" knowledge mark-stale "$stale_revision" --workspace personal --expected-state-revision 2 \
  --reason 'Stale for portable lifecycle coverage.' --socket "$active_socket" \
  --idempotency-key portable-stale --output json >"$scenario_root/stale.json"

old_revision=$(propose_accept predecessor "$source_task" 'Portable predecessor decision' 'This body is superseded.')
propose "$scenario_root/successor-proposed.json" "$source_task" 'Portable successor decision' 'This body is the current successor.' portable-successor --supersedes "$old_revision"
successor_revision=$(extract_id krev "$scenario_root/successor-proposed.json")
accept "$scenario_root/successor-accepted.json" "$successor_revision" portable-successor-accept

proposed_left=$(propose_accept proposed-left "$source_task" 'Portable proposed conflict left' 'Proposed conflict says left.')
proposed_right=$(propose_accept proposed-right "$source_task" 'Portable proposed conflict right' 'Proposed conflict says right.')
report_contradiction "$scenario_root/contradiction-proposed.json" "$proposed_left" "$proposed_right" 'Portable proposed contradiction.' portable-contradiction-proposed
proposed_contradiction=$(extract_id kcon "$scenario_root/contradiction-proposed.json")

dismissed_left=$(propose_accept dismissed-left "$source_task" 'Portable dismissed conflict left' 'Dismissed conflict says left.')
dismissed_right=$(propose_accept dismissed-right "$source_task" 'Portable dismissed conflict right' 'Dismissed conflict says right.')
report_contradiction "$scenario_root/contradiction-dismissed-proposed.json" "$dismissed_left" "$dismissed_right" 'Portable dismissed contradiction.' portable-contradiction-dismissed
dismissed_contradiction=$(extract_id kcon "$scenario_root/contradiction-dismissed-proposed.json")
"$binary" contradiction dismiss "$dismissed_contradiction" --workspace personal --expected-state-revision 1 \
  --note 'Dismissed for portable lifecycle coverage.' --socket "$active_socket" \
  --idempotency-key portable-dismiss --output json >"$scenario_root/contradiction-dismissed.json"

resolved_left=$(propose_accept resolved-left "$source_task" 'Portable resolved conflict left' 'Resolved conflict says left.')
resolved_right=$(propose_accept resolved-right "$source_task" 'Portable resolved conflict right' 'Resolved conflict says right.')
report_contradiction "$scenario_root/contradiction-resolved-proposed.json" "$resolved_left" "$resolved_right" 'Portable resolved contradiction.' portable-contradiction-resolved
resolved_contradiction=$(extract_id kcon "$scenario_root/contradiction-resolved-proposed.json")
"$binary" contradiction confirm "$resolved_contradiction" --workspace personal --expected-state-revision 1 \
  --note 'Open before deterministic resolution.' --socket "$active_socket" \
  --idempotency-key portable-resolved-confirm --output json >"$scenario_root/contradiction-resolved-open.json"
"$binary" knowledge mark-stale "$resolved_left" --workspace personal --expected-state-revision 2 \
  --reason 'Resolve the portable contradiction.' --socket "$active_socket" \
  --idempotency-key portable-resolved-stale --output json >"$scenario_root/contradiction-resolved.json"

report_contradiction "$scenario_root/contradiction-open-proposed.json" "$broad_revision" "$scoped_revision" 'Portable open contradiction.' portable-contradiction-open
open_contradiction=$(extract_id kcon "$scenario_root/contradiction-open-proposed.json")
"$binary" contradiction confirm "$open_contradiction" --workspace personal --expected-state-revision 1 \
  --note 'Keep this conflict open through transport.' --socket "$active_socket" \
  --idempotency-key portable-open-confirm --output json >"$scenario_root/contradiction-open.json"

for revision in "$broad_revision" "$scoped_revision" "$scoped_safe_revision" "$pending_revision" "$rejected_revision" "$stale_revision" "$old_revision" "$successor_revision"
do [ -n "$revision" ] || fail 'a required portable revision ID is missing'; done

bundle_one="$scenario_root/bundle-one"
bundle_two="$scenario_root/bundle-two"
"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$active_socket" --output json >"$scenario_root/source-events-before-export.json"
"$binary" knowledge export "$bundle_one" --workspace personal --project demo --socket "$active_socket" --output json >"$scenario_root/export-one.json"
"$binary" knowledge export "$bundle_two" --workspace personal --project demo --socket "$active_socket" --output json >"$scenario_root/export-two.json"
"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$active_socket" --output json >"$scenario_root/source-events-after-export.json"
cmp "$scenario_root/source-events-before-export.json" "$scenario_root/source-events-after-export.json" || fail 'portable export appended an event'
content_digest=$(extract_content_digest "$scenario_root/export-one.json")
[ ${#content_digest} -eq 64 ] || fail 'export did not return a full content digest'
cmp "$bundle_one/manifest.json" "$bundle_two/manifest.json" || fail 'unchanged manifest export was not deterministic'
cmp "$bundle_one/knowledge.md" "$bundle_two/knowledge.md" || fail 'unchanged Markdown export was not deterministic'
[ "$(find "$bundle_one" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort | tr '\n' ' ')" = 'knowledge.md manifest.json ' ] || fail 'bundle listing was not exactly the two specified files'
[ "$(stat -c '%a' "$bundle_one")" = 700 ] || fail 'bundle directory mode was not 0700'
[ "$(stat -c '%a' "$bundle_one/manifest.json")" = 600 ] || fail 'manifest mode was not 0600'
[ "$(stat -c '%a' "$bundle_one/knowledge.md")" = 600 ] || fail 'Markdown mode was not 0600'
for token in "$broad_revision" "$scoped_revision" "$scoped_safe_revision" "$pending_revision" "$rejected_revision" "$stale_revision" "$old_revision" "$successor_revision" "$proposed_contradiction" "$open_contradiction" "$dismissed_contradiction" "$resolved_contradiction" '"status":"proposed"' '"status":"open"' '"status":"dismissed"' '"status":"resolved"'
do assert_contains "$bundle_one/manifest.json" "$token" "portable manifest omitted $token"; done

assert_error knowledge_export_path_exists "$scenario_root/existing-export.err" \
  "$binary" knowledge export "$bundle_one" --workspace personal --project demo --socket "$active_socket" --output json

stop_daemon source-before-degraded
tamper_source="$scenario_root/drop-derived-index.go"
sed -e 's|@DRIVER@|github.com/ncruces/go-sqlite3/driver|' -e 's|@FTS@|github.com/ncruces/go-sqlite3/ext/fts5|' >"$tamper_source" <<'TAMPER'
package main
import (
  "fmt"
  "os"
  "@DRIVER@"
  "@FTS@"
)
func main() {
  database, err := driver.Open(os.Args[1], fts5.Register); if err != nil { panic(err) }
  defer database.Close()
  if len(os.Args) == 3 && os.Args[2] == "assert-no-operational" {
    var count int
    err = database.QueryRow(`SELECT
      (SELECT COUNT(*) FROM tasks) + (SELECT COUNT(*) FROM meetings) +
      (SELECT COUNT(*) FROM agents) + (SELECT COUNT(*) FROM runs) +
      (SELECT COUNT(*) FROM repositories) + (SELECT COUNT(*) FROM checkouts)`).Scan(&count)
    if err != nil { panic(err) }
    if count != 0 { panic(fmt.Sprintf("portable import fabricated %d operational rows", count)) }
    return
  }
  if len(os.Args) == 3 && os.Args[2] == "write-counts" {
    var workspaces, projects, anchors, bindings, items, revisions, sources, contradictions int
    var imports, importedEntities, events, idempotency, knowledgeAuthority, contradictionAuthority, derivations, autoAcceptances int
    err = database.QueryRow(`SELECT
      (SELECT COUNT(*) FROM workspaces), (SELECT COUNT(*) FROM projects),
      (SELECT COUNT(*) FROM knowledge_task_scope_anchors), (SELECT COUNT(*) FROM knowledge_item_task_scopes),
      (SELECT COUNT(*) FROM knowledge_items), (SELECT COUNT(*) FROM knowledge_revisions),
      (SELECT COUNT(*) FROM knowledge_sources), (SELECT COUNT(*) FROM knowledge_contradictions),
      (SELECT COUNT(*) FROM knowledge_imports), (SELECT COUNT(*) FROM knowledge_import_entities),
      (SELECT COUNT(*) FROM events), (SELECT COUNT(*) FROM idempotency_keys),
      (SELECT COUNT(*) FROM knowledge_authority_checks),
      (SELECT COUNT(*) FROM knowledge_contradiction_authority_checks),
      (SELECT COUNT(*) FROM curator_derivations), (SELECT COUNT(*) FROM curator_auto_acceptances)`).Scan(
        &workspaces, &projects, &anchors, &bindings, &items, &revisions, &sources, &contradictions,
        &imports, &importedEntities, &events, &idempotency, &knowledgeAuthority,
        &contradictionAuthority, &derivations, &autoAcceptances)
    if err != nil { panic(err) }
    fmt.Printf("%d %d %d %d %d %d %d %d %d %d %d %d %d %d %d %d\n",
      workspaces, projects, anchors, bindings, items, revisions, sources, contradictions,
      imports, importedEntities, events, idempotency, knowledgeAuthority,
      contradictionAuthority, derivations, autoAcceptances)
    return
  }
  if _, err := database.Exec("DROP TABLE knowledge_search"); err != nil { panic(err) }
}
TAMPER
GOTOOLCHAIN=local GOPROXY=off "$go_runner" run "$tamper_source" "$source_data/crewfold.db"
start_daemon "$source_data" "$source_socket"
"$binary" knowledge index status --workspace personal --socket "$active_socket" --output json >"$scenario_root/source-index-missing.json"
assert_contains "$scenario_root/source-index-missing.json" '"diagnosis":"missing"' 'failure injector did not remove only derived FTS'
bundle_degraded="$scenario_root/bundle-degraded"
"$binary" knowledge export "$bundle_degraded" --workspace personal --project demo --socket "$active_socket" --output json >"$scenario_root/export-degraded.json"
cmp "$bundle_one/manifest.json" "$bundle_degraded/manifest.json" || fail 'missing FTS changed portable manifest'
cmp "$bundle_one/knowledge.md" "$bundle_degraded/knowledge.md" || fail 'missing FTS changed portable Markdown'
propose "$scenario_root/changed-proposed.json" "$source_task" 'Portable later change' 'This valid later snapshot must not merge into an imported target.' portable-changed
changed_bundle="$scenario_root/bundle-changed"
"$binary" knowledge export "$changed_bundle" --workspace personal --project demo --socket "$active_socket" --output json >"$scenario_root/export-changed.json"
changed_digest=$(extract_content_digest "$scenario_root/export-changed.json")
[ ${#changed_digest} -eq 64 ] || fail 'changed export did not return a full content digest'
if cmp -s "$bundle_one/manifest.json" "$changed_bundle/manifest.json"; then fail 'changed canonical state produced the original bundle'; fi
stop_daemon source-final

start_daemon "$target_data" "$target_socket"
stop_daemon target-before-missing-index
GOTOOLCHAIN=local GOPROXY=off "$go_runner" run "$tamper_source" "$target_data/crewfold.db"
start_daemon "$target_data" "$target_socket"
"$binary" knowledge index status --workspace personal --socket "$active_socket" --output json >"$scenario_root/target-index-missing.json" 2>"$scenario_root/target-index-missing.err" || true
assert_contains "$scenario_root/target-index-missing.err" 'workspace_not_found' 'fresh target unexpectedly had a workspace before create-scope import'
GOTOOLCHAIN=local GOPROXY=off "$go_runner" run "$tamper_source" "$target_data/crewfold.db" write-counts >"$scenario_root/counts-before-missing-scope.txt"
assert_error knowledge_import_scope_conflict "$scenario_root/missing-scope.err" \
  "$binary" knowledge import "$bundle_one" --workspace personal --project demo --expected-content-sha256 "$content_digest" --idempotency-key portable-missing-scope --socket "$active_socket" --output json
GOTOOLCHAIN=local GOPROXY=off "$go_runner" run "$tamper_source" "$target_data/crewfold.db" write-counts >"$scenario_root/counts-after-missing-scope.txt"
cmp "$scenario_root/counts-before-missing-scope.txt" "$scenario_root/counts-after-missing-scope.txt" || fail 'rejected import into absent scope changed durable state'
"$binary" knowledge import "$bundle_one" --workspace personal --project demo --expected-content-sha256 "$content_digest" \
  --create-scope --idempotency-key portable-import --socket "$active_socket" --output json >"$scenario_root/import.json"
assert_contains "$scenario_root/import.json" '"status":"imported"' 'first portable import did not report imported'
assert_contains "$scenario_root/import.json" '"workspaces":1,"projects":1' 'create-scope did not report exact workspace/project creation'
assert_contains "$scenario_root/import.json" '"counts":{"task_scope_anchors":1,"items":13,"revisions":14,"contradictions":4}' 'import returned incorrect complete snapshot counts'
"$binary" knowledge index status --workspace personal --socket "$active_socket" --output json >"$scenario_root/target-index-after-import.json"
assert_contains "$scenario_root/target-index-after-import.json" '"diagnosis":"missing"' 'owner import unexpectedly depended on or recreated missing FTS'

"$binary" task list --workspace personal --project demo --socket "$active_socket" --output json >"$scenario_root/target-tasks.json"
"$binary" agent list --workspace personal --socket "$active_socket" --output json >"$scenario_root/target-agents.json"
"$binary" run list --workspace personal --socket "$active_socket" --output json >"$scenario_root/target-runs.json"
"$binary" project inspect demo --workspace personal --socket "$active_socket" --output json >"$scenario_root/target-project.json"
assert_absent "$scenario_root/target-tasks.json" '"id":"task_' 'import fabricated an operational task'
assert_absent "$scenario_root/target-agents.json" '"id":"agent_' 'import fabricated an agent'
assert_absent "$scenario_root/target-runs.json" '"id":"run_' 'import fabricated a run'
assert_contains "$scenario_root/target-project.json" '"repositories":[],"checkouts":[]' 'import fabricated repository or checkout state'
stop_daemon target-before-operational-audit
GOTOOLCHAIN=local GOPROXY=off "$go_runner" run "$tamper_source" "$target_data/crewfold.db" assert-no-operational
start_daemon "$target_data" "$target_socket"

for revision in "$broad_revision" "$scoped_revision" "$scoped_safe_revision" "$pending_revision" "$rejected_revision" "$stale_revision" "$old_revision" "$successor_revision"
do
  "$binary" knowledge show "$revision" --workspace personal --socket "$active_socket" --output json >"$scenario_root/show-$revision.json"
  assert_contains "$scenario_root/show-$revision.json" "$revision" "import omitted exact revision $revision"
done
assert_contains "$scenario_root/show-$scoped_revision.json" "\"task_scope_id\":\"$source_task\"" 'import lost exact task applicability anchor'
assert_contains "$scenario_root/show-$broad_revision.json" 'Use the portable broad route for all work.' 'import lost exact knowledge content'
assert_contains "$scenario_root/show-$broad_revision.json" '"review_status":"accepted","currency_status":"current"' 'import lost current accepted state'
assert_contains "$scenario_root/show-$broad_revision.json" "\"sources\":[{\"type\":\"task\",\"id\":\"$source_task\",\"revision\":1,\"role\":\"primary\",\"ordinal\":0},{\"type\":\"task\",\"id\":\"$support_task\",\"revision\":1,\"role\":\"supporting\",\"ordinal\":1}]" 'import lost ordered source snapshots'
assert_contains "$scenario_root/show-$pending_revision.json" '"review_status":"proposed","currency_status":"pending"' 'import lost proposed state'
assert_contains "$scenario_root/show-$rejected_revision.json" '"review_status":"rejected","currency_status":"pending"' 'import lost rejected state'
assert_contains "$scenario_root/show-$stale_revision.json" '"review_status":"accepted","currency_status":"stale"' 'import lost stale state'
assert_contains "$scenario_root/show-$old_revision.json" '"review_status":"accepted","currency_status":"superseded"' 'import lost superseded state'
assert_contains "$scenario_root/show-$successor_revision.json" '"review_status":"accepted","currency_status":"current"' 'import lost successor state'
assert_contains "$scenario_root/show-$successor_revision.json" '"authority_checks":[]' 'import forged origin knowledge authority evidence'
"$binary" contradiction show "$open_contradiction" --workspace personal --socket "$active_socket" --output json >"$scenario_root/target-open.json"
"$binary" contradiction show "$proposed_contradiction" --workspace personal --socket "$active_socket" --output json >"$scenario_root/target-proposed.json"
"$binary" contradiction show "$dismissed_contradiction" --workspace personal --socket "$active_socket" --output json >"$scenario_root/target-dismissed.json"
"$binary" contradiction show "$resolved_contradiction" --workspace personal --socket "$active_socket" --output json >"$scenario_root/target-resolved.json"
assert_contains "$scenario_root/target-open.json" '"status":"open"' 'import lost open contradiction state'
assert_contains "$scenario_root/target-proposed.json" '"status":"proposed"' 'import lost proposed contradiction state'
assert_contains "$scenario_root/target-dismissed.json" '"status":"dismissed"' 'import lost dismissed contradiction state'
assert_contains "$scenario_root/target-resolved.json" '"status":"resolved"' 'import lost resolved contradiction state'
assert_contains "$scenario_root/target-open.json" '"authority_check_count":0,"authority_checks":[]' 'import forged origin contradiction authority evidence'
"$binary" knowledge dispute "$broad_revision" --workspace personal --socket "$active_socket" --output json >"$scenario_root/target-dispute.json"
assert_contains "$scenario_root/target-dispute.json" "\"disputed\":true,\"open_contradiction_count\":1,\"open_contradiction_ids\":[\"$open_contradiction\"]" 'imported open contradiction did not take effect'

target_export="$scenario_root/target-export"
"$binary" knowledge export "$target_export" --workspace personal --project demo --socket "$active_socket" --output json >"$scenario_root/target-export.json"
cmp "$bundle_one/manifest.json" "$target_export/manifest.json" || fail 'immediate re-export changed canonical manifest bytes'
cmp "$bundle_one/knowledge.md" "$target_export/knowledge.md" || fail 'immediate re-export changed Markdown bytes'

"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$active_socket" --output json >"$scenario_root/events-before-replay.json"
assert_occurrences "$scenario_root/events-before-replay.json" '"type":"knowledge.imported"' 14 'import emitted the wrong number of revision attestations'
assert_occurrences "$scenario_root/events-before-replay.json" '"type":"contradiction.imported"' 4 'import emitted the wrong number of contradiction attestations'
assert_occurrences "$scenario_root/events-before-replay.json" '"type":"knowledge.import_completed"' 1 'import emitted the wrong number of completion attestations'
for origin_event in '"type":"knowledge.proposed"' '"type":"knowledge.accepted"' '"type":"contradiction.detected"' '"type":"task.created"' '"type":"meeting.created"'
do assert_absent "$scenario_root/events-before-replay.json" "$origin_event" "import replayed excluded origin event $origin_event"; done
"$binary" knowledge import "$bundle_one" --workspace personal --project demo --expected-content-sha256 "$content_digest" \
  --create-scope --idempotency-key portable-import --socket "$active_socket" --output json >"$scenario_root/import-replay.json"
"$binary" knowledge import "$bundle_one" --workspace personal --project demo --expected-content-sha256 "$content_digest" \
  --create-scope --idempotency-key portable-import-new-key --socket "$active_socket" --output json >"$scenario_root/import-new-key.json"
assert_contains "$scenario_root/import-replay.json" '"status":"already_present"' 'same-key replay was not recognized'
assert_contains "$scenario_root/import-new-key.json" '"status":"already_present"' 'new-key exact replay was not recognized'
assert_contains "$scenario_root/import-replay.json" '"created":{"workspaces":0,"projects":0,"task_scope_anchors":0}' 'same-key replay repeated scope creation'
assert_contains "$scenario_root/import-new-key.json" '"created":{"workspaces":0,"projects":0,"task_scope_anchors":0}' 'new-key replay repeated scope creation'
"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$active_socket" --output json >"$scenario_root/events-after-replay.json"
cmp "$scenario_root/events-before-replay.json" "$scenario_root/events-after-replay.json" || fail 'exact import replay appended an event'

"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$active_socket" --output json >"$scenario_root/events-before-invalid-imports.json"
GOTOOLCHAIN=local GOPROXY=off "$go_runner" run "$tamper_source" "$target_data/crewfold.db" write-counts >"$scenario_root/counts-before-invalid-imports.txt"
assert_error idempotency_conflict "$scenario_root/reused-key-changed-request.err" \
  "$binary" knowledge import "$bundle_one" --workspace personal --project demo --expected-content-sha256 "$content_digest" --idempotency-key portable-import --socket "$active_socket" --output json
assert_error knowledge_bundle_digest_mismatch "$scenario_root/wrong-digest.err" \
  "$binary" knowledge import "$bundle_one" --workspace personal --project demo --expected-content-sha256 0000000000000000000000000000000000000000000000000000000000000000 --idempotency-key portable-wrong-digest --socket "$active_socket" --output json
assert_error knowledge_import_scope_conflict "$scenario_root/wrong-scope.err" \
  "$binary" knowledge import "$bundle_one" --workspace wrong --project demo --expected-content-sha256 "$content_digest" --create-scope --idempotency-key portable-wrong-scope --socket "$active_socket" --output json

extra_bundle="$scenario_root/extra-bundle"
cp -R "$bundle_one" "$extra_bundle"
printf 'extra\n' >"$extra_bundle/extra.txt"
assert_error invalid_knowledge_bundle "$scenario_root/extra-file.err" \
  "$binary" knowledge import "$extra_bundle" --workspace personal --project demo --expected-content-sha256 "$content_digest" --idempotency-key portable-extra --socket "$active_socket" --output json
missing_bundle="$scenario_root/missing-bundle"
cp -R "$bundle_one" "$missing_bundle"
find "$missing_bundle" -type f -name knowledge.md -delete
assert_error invalid_knowledge_bundle "$scenario_root/missing-file.err" \
  "$binary" knowledge import "$missing_bundle" --workspace personal --project demo --expected-content-sha256 "$content_digest" --idempotency-key portable-missing --socket "$active_socket" --output json
tampered_bundle="$scenario_root/tampered-bundle"
cp -R "$bundle_one" "$tampered_bundle"
printf '\nTAMPERED\n' >>"$tampered_bundle/knowledge.md"
assert_error knowledge_bundle_digest_mismatch "$scenario_root/tampered.err" \
  "$binary" knowledge import "$tampered_bundle" --workspace personal --project demo --expected-content-sha256 "$content_digest" --idempotency-key portable-tampered --socket "$active_socket" --output json
ln -s "$bundle_one" "$scenario_root/bundle-link"
assert_error invalid_knowledge_bundle_path "$scenario_root/symlink.err" \
  "$binary" knowledge import "$scenario_root/bundle-link" --workspace personal --project demo --expected-content-sha256 "$content_digest" --idempotency-key portable-link --socket "$active_socket" --output json
assert_error knowledge_import_conflict "$scenario_root/nonempty-target.err" \
  "$binary" knowledge import "$changed_bundle" --workspace personal --project demo --expected-content-sha256 "$changed_digest" --idempotency-key portable-changed-import --socket "$active_socket" --output json
"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$active_socket" --output json >"$scenario_root/events-after-invalid-imports.json"
cmp "$scenario_root/events-before-invalid-imports.json" "$scenario_root/events-after-invalid-imports.json" || fail 'rejected portable imports appended an event'
GOTOOLCHAIN=local GOPROXY=off "$go_runner" run "$tamper_source" "$target_data/crewfold.db" write-counts >"$scenario_root/counts-after-invalid-imports.txt"
cmp "$scenario_root/counts-before-invalid-imports.txt" "$scenario_root/counts-after-invalid-imports.txt" || fail 'rejected portable imports changed durable canonical/audit state'

"$binary" knowledge index rebuild --workspace personal --socket "$active_socket" --idempotency-key portable-target-index --output json >"$scenario_root/target-index.json"
"$binary" knowledge search 'portable' --workspace personal --project demo --limit 100 --socket "$active_socket" --output json >"$scenario_root/target-search.json"
assert_absent "$scenario_root/target-search.json" "$scoped_revision" 'project-only search leaked imported task-scoped knowledge'
assert_absent "$scenario_root/target-search.json" "$scoped_safe_revision" 'project-only search leaked independently safe task-scoped knowledge'
assert_absent "$scenario_root/target-search.json" "$broad_revision" 'search leaked imported open-contradiction participant'
assert_contains "$scenario_root/target-search.json" "$successor_revision" 'rebuilt target search omitted safe current imported knowledge'

"$binary" checkout add demo "$fixture_root/world-engine-2" --workspace personal --mode exclusive \
  --socket "$active_socket" --idempotency-key portable-target-checkout --output json >"$scenario_root/target-checkout.json"
"$binary" agent create portable-context --workspace personal --role reviewer --provider fake --runtime fake \
  --socket "$active_socket" --idempotency-key portable-target-agent --output json >"$scenario_root/target-agent.json"
create_task "$scenario_root/target-context-task.json" 'Consume imported broad knowledge' portable-target-context-task
target_task=$(extract_id task "$scenario_root/target-context-task.json")
"$binary" task assign "$target_task" portable-context --workspace personal --lease-seconds 600 --expected-revision 1 \
  --socket "$active_socket" --idempotency-key portable-target-assign --output json >"$scenario_root/target-assigned.json"
"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$active_socket" --output json >"$scenario_root/events-before-context.json"
assert_error knowledge_conflict "$scenario_root/context-conflict.err" \
  "$binary" context build "$target_task" --workspace personal --agent portable-context --expected-task-revision 2 \
  --include "$broad_revision" --socket "$active_socket" --idempotency-key portable-target-context --output json
"$binary" events list --workspace personal --after 0 --limit 1000 --socket "$active_socket" --output json >"$scenario_root/events-after-context.json"
cmp "$scenario_root/events-before-context.json" "$scenario_root/events-after-context.json" || fail 'imported dispute context failure appended an event'

stop_daemon target-restart
start_daemon "$target_data" "$target_socket"
"$binary" knowledge show "$successor_revision" --workspace personal --socket "$active_socket" --output json >"$scenario_root/target-after-restart.json"
assert_contains "$scenario_root/target-after-restart.json" "$successor_revision" 'imported knowledge did not survive restart'
"$binary" contradiction show "$open_contradiction" --workspace personal --socket "$active_socket" --output json >"$scenario_root/open-after-restart.json"
assert_contains "$scenario_root/open-after-restart.json" '"status":"open"' 'imported contradiction did not survive restart'
stop_daemon target-final

printf 'Portable project knowledge acceptance: PASS\n'
