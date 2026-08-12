#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
go_runner="$repo_root/scripts/go.sh"
cd "$repo_root"

export GOTOOLCHAIN=local
export GOPROXY=off

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
