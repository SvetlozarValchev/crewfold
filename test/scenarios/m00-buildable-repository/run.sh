#!/bin/sh
set -eu

scenario_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$scenario_dir/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
temp_dir=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-m00.XXXXXX")

cleanup() {
  if [ -n "$temp_dir" ] && [ -d "$temp_dir" ]
  then
    find "$temp_dir" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

cd "$repo_root"
export GOTOOLCHAIN=local
export GOPROXY=off

binary="$temp_dir/crewfold"
$go_runner build -trimpath -o "$binary" ./cmd/crewfold

"$binary" help >"$temp_dir/help.txt"
grep -Fq 'Usage:' "$temp_dir/help.txt"
grep -Fq 'version' "$temp_dir/help.txt"
grep -Fq 'doctor --self' "$temp_dir/help.txt"

"$binary" version >"$temp_dir/version.txt"
grep -Fq 'crewfold dev' "$temp_dir/version.txt"
grep -Fq 'platform:' "$temp_dir/version.txt"

"$binary" version --output json >"$temp_dir/version.json"
grep -Fq '"schema":"urn:crewfold:schema:cli:version-response:v1"' "$temp_dir/version.json"
grep -Fq '"version":"dev"' "$temp_dir/version.json"

release_binary="$temp_dir/crewfold-release"
$go_runner build -trimpath \
  -ldflags '-X crewfold/internal/buildinfo.version=0.0.0-m0 -X crewfold/internal/buildinfo.commit=acceptance-commit -X crewfold/internal/buildinfo.builtAt=2026-08-12T00:00:00Z' \
  -o "$release_binary" ./cmd/crewfold
"$release_binary" version --output json >"$temp_dir/release-version.json"
grep -Fq '"version":"0.0.0-m0"' "$temp_dir/release-version.json"
grep -Fq '"commit":"acceptance-commit"' "$temp_dir/release-version.json"
grep -Fq '"built_at":"2026-08-12T00:00:00Z"' "$temp_dir/release-version.json"
"$release_binary" doctor --self >"$temp_dir/release-doctor.txt"
grep -Fq 'status: ok' "$temp_dir/release-doctor.txt"

"$binary" doctor --self >"$temp_dir/doctor.txt"
grep -Fq 'status: ok' "$temp_dir/doctor.txt"

"$binary" doctor --self --output json >"$temp_dir/doctor.json"
grep -Fq '"schema":"urn:crewfold:schema:cli:doctor-self-response:v1"' "$temp_dir/doctor.json"
grep -Fq '"status":"ok"' "$temp_dir/doctor.json"

set +e
"$binary" definitely-not-a-command >"$temp_dir/unknown.stdout" 2>"$temp_dir/unknown.stderr"
unknown_exit=$?
set -e
if [ "$unknown_exit" -ne 2 ]
then
  printf 'unknown command exit code = %s, want 2\n' "$unknown_exit" >&2
  exit 1
fi
grep -Fq 'error: unknown command "definitely-not-a-command"' "$temp_dir/unknown.stderr"
grep -Fq "hint: run 'crewfold help'" "$temp_dir/unknown.stderr"
if grep -Eq 'panic:|goroutine [0-9]+' "$temp_dir/unknown.stderr"
then
  printf 'unknown command emitted a stack trace\n' >&2
  exit 1
fi

set +e
"$binary" --output json definitely-not-a-command >"$temp_dir/unknown-json.stdout" 2>"$temp_dir/unknown-json.stderr"
unknown_json_exit=$?
set -e
if [ "$unknown_json_exit" -ne 2 ]
then
  printf 'JSON unknown command exit code = %s, want 2\n' "$unknown_json_exit" >&2
  exit 1
fi
if [ -s "$temp_dir/unknown-json.stdout" ]
then
  printf 'JSON unknown command unexpectedly wrote to stdout\n' >&2
  exit 1
fi
grep -Fq '"code":"unknown_command"' "$temp_dir/unknown-json.stderr"

printf 'M0 acceptance: PASS\n'
