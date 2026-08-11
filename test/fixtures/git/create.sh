#!/bin/sh
set -eu

if [ "$#" -ne 1 ]
then
  printf 'usage: %s <fixture-root>\n' "$0" >&2
  exit 2
fi

fixture_root=$1
main_checkout="$fixture_root/world-engine"
adjacent_two="$fixture_root/world-engine-2"
adjacent_five="$fixture_root/world-engine-5"
linked_checkout="$fixture_root/world-engine-linked"

mkdir -p "$fixture_root"
git init --quiet --initial-branch=main "$main_checkout"
git -C "$main_checkout" config user.name 'Crewfold Fixture'
git -C "$main_checkout" config user.email 'fixture@crewfold.invalid'
printf 'fixture\n' >"$main_checkout/README.md"
git -C "$main_checkout" add README.md
GIT_AUTHOR_DATE='2026-01-01T00:00:00Z' \
GIT_COMMITTER_DATE='2026-01-01T00:00:00Z' \
  git -C "$main_checkout" commit --quiet -m 'fixture root'

git clone --quiet --no-hardlinks "$main_checkout" "$adjacent_two"
git -C "$adjacent_two" checkout --quiet -b agent-two
git clone --quiet --no-hardlinks "$main_checkout" "$adjacent_five"
git -C "$adjacent_five" checkout --quiet -b agent-five
git -C "$main_checkout" worktree add --quiet -b agent-linked "$linked_checkout"

printf '%s\n' "$main_checkout" "$adjacent_two" "$adjacent_five" "$linked_checkout"
