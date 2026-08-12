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

find internal/store/dbgen -type f -name '*.go' -print | LC_ALL=C sort | while IFS= read -r generated_path
do
  hash_file "$generated_path"
done | hash_stream | awk '{print $1}'
