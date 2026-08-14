#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
test_root=$(mktemp -d)
cleanup() {
  rm -rf -- "$test_root"
}
trap cleanup EXIT

"$repo_root/scripts/package-linux.sh" "$test_root/first" >/dev/null
"$repo_root/scripts/package-linux.sh" "$test_root/second" >/dev/null
archive=crewfold-linux-amd64.tar.gz
cmp "$test_root/first/$archive" "$test_root/second/$archive"
cmp "$test_root/first/$archive.sha256" "$test_root/second/$archive.sha256"
(
  cd "$test_root/first"
  sha256sum -c "$archive.sha256"
)
mkdir "$test_root/extracted"
tar -xzf "$test_root/first/$archive" -C "$test_root/extracted"
binary="$test_root/extracted/crewfold-linux-amd64/crewfold"
"$binary" version >/dev/null
"$binary" doctor --self >/dev/null

echo "Reproducible unpublished Linux candidate: PASS"
