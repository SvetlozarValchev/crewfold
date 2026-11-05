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

host_root=crewfold-linux-amd64
connector_root=crewfold-connector-linux-amd64
host_archive=$host_root.tar.gz
connector_archive=$connector_root.tar.gz
mkdir -m 0755 "$stage/$host_root" "$stage/$connector_root"

go_version=$(sed -n '1p' "$repo_root/.go-version")
tool_root=${XDG_DATA_HOME:-$HOME/.local/share}/crewfold-dev/toolchains/go$go_version/bin
go_binary=$tool_root/go
if [[ ! -x "$go_binary" ]]; then
  echo "pinned Go toolchain is unavailable: $go_binary" >&2
  exit 1
fi

release_version=${CREWFOLD_VERSION:-dev}
release_commit=${CREWFOLD_COMMIT:-$(git -C "$repo_root" rev-parse HEAD)}
release_built_at=${CREWFOLD_BUILT_AT:-$(git -C "$repo_root" show -s --format=%cI HEAD)}
if [[ ! "$release_version" =~ ^[0-9A-Za-z._+-]+$ || ! "$release_commit" =~ ^[0-9A-Fa-f]+$ || ! "$release_built_at" =~ ^[0-9TZ:.+-]+$ ]]; then
  echo "release build metadata contains unsupported characters" >&2
  exit 1
fi
release_ldflags="-buildid= -X crewfold/internal/buildinfo.version=$release_version -X crewfold/internal/buildinfo.commit=$release_commit -X crewfold/internal/buildinfo.builtAt=$release_built_at"

(
  cd "$repo_root"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=local GOPROXY=off "$go_binary" build \
    -trimpath -buildvcs=false -ldflags="$release_ldflags" \
    -o "$stage/$host_root/crewfold" ./cmd/crewfold
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=local GOPROXY=off "$go_binary" build \
    -trimpath -buildvcs=false -ldflags="$release_ldflags" \
    -o "$stage/$connector_root/crewfold-connector" ./cmd/crewfold-connector
)
chmod 0755 "$stage/$host_root/crewfold" "$stage/$connector_root/crewfold-connector"
cp "$repo_root/README.md" "$stage/$host_root/README.md"
cp "$repo_root/docs/connector.md" "$stage/$connector_root/README.md"
chmod 0644 "$stage/$host_root/README.md" "$stage/$connector_root/README.md"

tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner \
  --pax-option=delete=atime,delete=ctime -C "$stage" -cf - "$host_root" | gzip -n -9 >"$stage/$host_archive"
tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner \
  --pax-option=delete=atime,delete=ctime -C "$stage" -cf - "$connector_root" | gzip -n -9 >"$stage/$connector_archive"
(
  cd "$stage"
  sha256sum "$host_archive" >"$host_archive.sha256"
  sha256sum "$connector_archive" >"$connector_archive.sha256"
)
mkdir -m 0755 "$stage/publish"
mv "$stage/$host_archive" "$stage/$host_archive.sha256" "$stage/$connector_archive" "$stage/$connector_archive.sha256" "$stage/publish/"
mkdir -m 0755 "$output_dir"
output_created=1
mv "$stage/publish/$host_archive" "$stage/publish/$host_archive.sha256" \
  "$stage/publish/$connector_archive" "$stage/publish/$connector_archive.sha256" "$output_dir/"
output_created=0

echo "$output_dir/$host_archive"
echo "$output_dir/$host_archive.sha256"
echo "$output_dir/$connector_archive"
echo "$output_dir/$connector_archive.sha256"
