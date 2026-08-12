#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
fixture_state=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-herdr-endpoint.XXXXXX")

cleanup() {
  if [ -d "$fixture_state" ]
  then
    find "$fixture_state" -depth -delete
  fi
}
trap cleanup EXIT HUP INT TERM

export CREWFOLD_HERDR_BINARY="$repo_root/test/fixtures/runtimes/herdr/herdr"
export CREWFOLD_HERDR_SESSION=crewfold-acceptance
export CREWFOLD_FIXTURE_HERDR_STATE="$fixture_state"
export CREWFOLD_FIXTURE_HERDR_SCHEMA="$repo_root/test/fixtures/protocol/herdr/schema-compatible.json"
export CREWFOLD_ACCEPTANCE_HERDR_INCOMPATIBLE_SCHEMA="$repo_root/test/fixtures/protocol/herdr/schema-incompatible.json"
export CREWFOLD_ACCEPTANCE_RUNTIME=herdr
export CREWFOLD_ACCEPTANCE_PROVIDER=fixture-terminal
export CREWFOLD_ACCEPTANCE_WAKE=succeeded

"$repo_root/test/scenarios/agent-messaging/run.sh"
printf 'Herdr runtime acceptance: PASS\n'
