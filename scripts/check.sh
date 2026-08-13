#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
go_runner="$repo_root/scripts/go.sh"
cd "$repo_root"

export GOTOOLCHAIN=local
export GOPROXY=off

printf 'Generated database query consistency\n'
"$repo_root/scripts/check-generated-db.sh"

go_root=$($go_runner env GOROOT)
gofmt="$go_root/bin/gofmt"
unformatted=$(find cmd internal protocol -type f -name '*.go' -print0 | xargs -0 "$gofmt" -l)
if [ -n "$unformatted" ]
then
  printf 'The following Go files need gofmt:\n%s\n' "$unformatted" >&2
  exit 1
fi

printf 'go vet ./...\n'
$go_runner vet ./...

printf 'go test ./...\n'
$go_runner test ./...

if [ "$($go_runner env CGO_ENABLED)" = "1" ] && command -v gcc >/dev/null 2>&1
then
  printf 'go test -race ./...\n'
  $go_runner test -race ./...
else
  printf 'go test -race ./... skipped: race detector prerequisites unavailable\n'
fi

printf 'Buildable repository black-box acceptance\n'
"$repo_root/test/scenarios/buildable-repository/run.sh"

printf 'Daemon API spine black-box acceptance\n'
"$repo_root/test/scenarios/daemon-api-spine/run.sh"

printf 'Persistent workspace black-box acceptance\n'
"$repo_root/test/scenarios/persistent-workspace/run.sh"

printf 'Projects and checkouts black-box acceptance\n'
"$repo_root/test/scenarios/projects-checkouts/run.sh"

printf 'Durable coordination black-box acceptance\n'
"$repo_root/test/scenarios/durable-coordination/run.sh"

printf 'Deterministic execution black-box acceptance\n'
"$repo_root/test/scenarios/deterministic-execution/run.sh"

printf 'Direct subprocess black-box acceptance\n'
"$repo_root/test/scenarios/direct-runtime/run.sh"

printf 'Scoped MCP capability black-box acceptance\n'
"$repo_root/test/scenarios/scoped-mcp/run.sh"

printf 'Durable agent messaging black-box acceptance\n'
"$repo_root/test/scenarios/agent-messaging/run.sh"

printf 'Participant-bound cross-project collaboration black-box acceptance\n'
"$repo_root/test/scenarios/cross-project-collaboration/run.sh"

printf 'Claims, overlap, and drift black-box acceptance\n'
"$repo_root/test/scenarios/claims-overlap/run.sh"

printf 'Structured meeting black-box acceptance\n'
"$repo_root/test/scenarios/structured-meetings/run.sh"

printf 'Canonical knowledge and provider-switch black-box acceptance\n'
"$repo_root/test/scenarios/canonical-knowledge/run.sh"

printf 'Deterministic knowledge retrieval black-box acceptance\n'
"$repo_root/test/scenarios/deterministic-knowledge-retrieval/run.sh"

printf 'Bounded deterministic curator black-box acceptance\n'
"$repo_root/test/scenarios/bounded-curator/run.sh"

printf 'Owner-confirmed knowledge contradiction black-box acceptance\n'
"$repo_root/test/scenarios/knowledge-contradictions/run.sh"

printf 'Portable project knowledge black-box acceptance\n'
"$repo_root/test/scenarios/portable-knowledge/run.sh"

printf 'Live context delta black-box acceptance\n'
"$repo_root/test/scenarios/live-context-deltas/run.sh"

printf 'Manager proposals and deterministic supervisor public smoke\n'
"$repo_root/test/scenarios/manager-supervisor/run.sh"

printf 'Owner-granted local checks and arbitrary-role check-watch acceptance\n'
"$repo_root/test/scenarios/local-checks/run.sh"

printf 'Herdr runtime black-box acceptance\n'
"$repo_root/test/scenarios/herdr-runtime/run.sh"

printf 'Codex provider black-box acceptance\n'
"$repo_root/test/scenarios/codex-provider/run.sh"

printf 'Claude provider and cross-provider handoff black-box acceptance\n'
"$repo_root/test/scenarios/claude-provider/run.sh"
