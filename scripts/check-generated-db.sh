#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

expected=$("$repo_root/scripts/db-source-hash.sh")
actual=$(sed -n '1p' internal/store/dbgen/.source-hash 2>/dev/null || true)

if [ "$actual" != "$expected" ]
then
  printf 'Generated database code is stale; run ./scripts/generate-db.sh\n' >&2
  exit 1
fi

expected_output=$(sed -n '1p' internal/store/dbgen/.output-hash 2>/dev/null || true)
actual_output=$("$repo_root/scripts/db-output-hash.sh")
if [ "$actual_output" != "$expected_output" ]
then
  printf 'Generated database code was modified; run ./scripts/generate-db.sh\n' >&2
  exit 1
fi
