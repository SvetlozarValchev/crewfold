#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

test -f web/dist/index.html
test -f web/dist/.source-sha256

expected=$(
  {
    find web/src -type f -print
    printf '%s\n' web/index.html web/package.json web/pnpm-lock.yaml web/tsconfig.json web/tsconfig.app.json web/tsconfig.node.json web/vite.config.ts
  } |
    LC_ALL=C sort |
    xargs sha256sum |
    sha256sum |
    cut -d ' ' -f 1
)
actual=$(sed -n '1p' web/dist/.source-sha256)
if [ "$actual" != "$expected" ]
then
  printf 'embedded room console assets are stale; run ./scripts/build-web.sh\n' >&2
  exit 1
fi

asset_count=0
for asset in $(sed -n 's/.*\(\/assets\/[^"<]*\).*/\1/p' web/dist/index.html)
do
  test -f "web/dist$asset"
  asset_count=$((asset_count + 1))
done
if [ "$asset_count" -lt 2 ]
then
  printf 'embedded room console index does not reference its content-hashed assets\n' >&2
  exit 1
fi

if find web/dist -type l -o -type p -o -type s | grep -q .
then
  printf 'embedded room console tree contains an unsafe non-regular entry\n' >&2
  exit 1
fi
if find web/dist -type f -name '*.map' | grep -q .
then
  printf 'embedded production room console contains source maps\n' >&2
  exit 1
fi
total_bytes=$(find web/dist -type f -printf '%s\n' | awk '{ total += $1 } END { print total + 0 }')
if [ "$total_bytes" -gt 5242880 ]
then
  printf 'embedded room console exceeds the 5 MiB asset limit: %s bytes\n' "$total_bytes" >&2
  exit 1
fi

printf 'Embedded room console assets: PASS (%s bytes)\n' "$total_bytes"
