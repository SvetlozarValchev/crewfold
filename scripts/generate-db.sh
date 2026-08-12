#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

"$repo_root/scripts/sqlc.sh" generate

source_hash=$("$repo_root/scripts/db-source-hash.sh")
printf '%s\n' "$source_hash" > internal/store/dbgen/.source-hash
output_hash=$("$repo_root/scripts/db-output-hash.sh")
printf '%s\n' "$output_hash" > internal/store/dbgen/.output-hash
