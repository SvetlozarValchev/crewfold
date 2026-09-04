#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

(
  cd web
  corepack pnpm install --frozen-lockfile
  corepack pnpm run check
  corepack pnpm run build
)

source_hash=$(
  {
    find web/src -type f -print
    printf '%s\n' web/index.html web/package.json web/pnpm-lock.yaml web/tsconfig.json web/tsconfig.app.json web/tsconfig.node.json web/vite.config.ts
  } |
    LC_ALL=C sort |
    xargs sha256sum |
    awk '{ print $1 }' |
    sha256sum |
    cut -d ' ' -f 1
)
printf '%s\n' "$source_hash" >web/dist/.source-sha256

printf 'Crewfold room console assets built: %s\n' "$source_hash"
