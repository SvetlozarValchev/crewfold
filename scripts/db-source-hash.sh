#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

hash_file() {
  if command -v sha256sum >/dev/null 2>&1
  then
    sha256sum "$1"
  else
    shasum -a 256 "$1"
  fi
}

hash_stream() {
  if command -v sha256sum >/dev/null 2>&1
  then
    sha256sum
  else
    shasum -a 256
  fi
}

{
  printf '%s\n' .sqlc-version sqlc.yaml scripts/sqlc-schema.sh
  find internal/store/migrations internal/store/queries -type f -name '*.sql' -print
} | LC_ALL=C sort | while IFS= read -r source_path
do
  hash_file "$source_path"
done | hash_stream | awk '{print $1}'
