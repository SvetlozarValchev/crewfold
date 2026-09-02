#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
go_runner="$repo_root/scripts/go.sh"
cd "$repo_root"

export GOTOOLCHAIN=local
export GOPROXY=off

printf 'Embedded room console asset consistency\n'
"$repo_root/scripts/check-web-assets.sh"

go_root=$($go_runner env GOROOT)
gofmt="$go_root/bin/gofmt"
unformatted=$(find cmd internal -type f -name '*.go' -print0 | xargs -0 "$gofmt" -l)
if [ -n "$unformatted" ]
then
  printf 'The following Go files need gofmt:\n%s\n' "$unformatted" >&2
  exit 1
fi

printf 'TypeScript room console\n'
(
  cd web
  corepack pnpm run check
)

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

printf 'production binary\n'
build_dir=$(mktemp -d)
trap 'rm -rf -- "$build_dir"' EXIT HUP INT TERM
$go_runner build -o "$build_dir/crewfold" ./cmd/crewfold
