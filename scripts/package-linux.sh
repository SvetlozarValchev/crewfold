#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || -z "${1//[[:space:]]/}" ]]; then
  echo "usage: ./scripts/package-linux.sh <new-output-directory>" >&2
  exit 2
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
output_parent=$(dirname -- "$1")
output_name=$(basename -- "$1")
if [[ ! -d "$output_parent" || -L "$output_parent" ]]; then
  echo "package output parent must be an existing real directory" >&2
  exit 2
fi
output_parent=$(CDPATH= cd -- "$output_parent" && pwd)
output_dir="$output_parent/$output_name"
if [[ -e "$output_dir" || -L "$output_dir" ]]; then
  echo "package output directory already exists: $output_dir" >&2
  exit 1
fi

stage=$(mktemp -d "$output_parent/.crewfold-package.XXXXXX")
output_created=0
cleanup() {
  rm -rf -- "$stage"
  if [[ $output_created -eq 1 ]]; then
    rm -rf -- "$output_dir"
  fi
}
trap cleanup EXIT

root_name=crewfold-linux-amd64
archive_name=$root_name.tar.gz
mkdir -m 0755 "$stage/$root_name"

go_version=$(sed -n '1p' "$repo_root/.go-version")
tool_root=${XDG_DATA_HOME:-$HOME/.local/share}/crewfold-dev/toolchains/go$go_version/bin
go_binary=$tool_root/go
if [[ ! -x "$go_binary" ]]; then
  echo "pinned Go toolchain is unavailable: $go_binary" >&2
  exit 1
fi

(
  cd "$repo_root"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=local GOPROXY=off "$go_binary" build \
    -trimpath -buildvcs=false -ldflags='-buildid=' \
    -o "$stage/$root_name/crewfold" ./cmd/crewfold
)
chmod 0755 "$stage/$root_name/crewfold"
cp "$repo_root/README.md" "$stage/$root_name/README.md"
chmod 0644 "$stage/$root_name/README.md"

tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner \
  --pax-option=delete=atime,delete=ctime \
  -C "$stage" -cf - "$root_name" | gzip -n -9 >"$stage/$archive_name"
(
  cd "$stage"
  sha256sum "$archive_name" >"$archive_name.sha256"
)
mkdir -m 0755 "$stage/publish"
mv "$stage/$archive_name" "$stage/$archive_name.sha256" "$stage/publish/"
mkdir -m 0755 "$output_dir"
output_created=1
mv "$stage/publish/$archive_name" "$stage/publish/$archive_name.sha256" "$output_dir/"
output_created=0

echo "$output_dir/$archive_name"
echo "$output_dir/$archive_name.sha256"
