#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
required_version=$(sed -n '1p' "$repo_root/.sqlc-version")

if command -v sqlc >/dev/null 2>&1
then
  sqlc_binary=$(command -v sqlc)
elif [ -x "$repo_root/.tools/sqlc-$required_version" ]
then
  sqlc_binary="$repo_root/.tools/sqlc-$required_version"
else
  printf 'sqlc %s is required to regenerate typed database queries.\n' "$required_version" >&2
  printf 'Install it from https://docs.sqlc.dev/en/stable/overview/install.html\n' >&2
  exit 1
fi

actual_version=$($sqlc_binary version | sed 's/^v//')
if [ "$actual_version" != "$required_version" ]
then
  printf 'sqlc %s is required; found %s at %s\n' "$required_version" "$actual_version" "$sqlc_binary" >&2
  exit 1
fi

exec "$sqlc_binary" "$@"
