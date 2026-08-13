#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

schema_dir=$(mktemp -d "$repo_root/.sqlc-schema.XXXXXX")
config_file=$(mktemp "$repo_root/.sqlc-config.XXXXXX.yaml")
schema_relative=${schema_dir#"$repo_root"/}
cleanup() {
    rm -rf "$schema_dir"
    rm -f "$config_file"
}
trap cleanup EXIT HUP INT TERM

"$repo_root/scripts/sqlc-schema.sh" "$schema_dir"
sed "s#schema: \"internal/store/migrations\"#schema: \"$schema_relative\"#" \
    "$repo_root/sqlc.yaml" > "$config_file"
"$repo_root/scripts/sqlc.sh" generate -f "$config_file"

source_hash=$("$repo_root/scripts/db-source-hash.sh")
printf '%s\n' "$source_hash" > internal/store/dbgen/.source-hash
output_hash=$("$repo_root/scripts/db-output-hash.sh")
printf '%s\n' "$output_hash" > internal/store/dbgen/.output-hash
