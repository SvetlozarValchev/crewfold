#!/bin/sh
set -eu

mode=${1:-}
case "$mode" in
  state)
    state=$(sed -n '1p' check-state.txt)
    case "$state" in
      PASS)
        printf 'state check passed\n'
        ;;
      FAIL)
        # Exercise the redacted artifact path without depending on a real secret.
        printf 'token=fixture-local-check-secret\n' >&2
        exit 7
        ;;
      *)
        printf 'unsupported fixture state: %s\n' "$state" >&2
        exit 8
        ;;
    esac
    ;;
  recovery)
    if [ "$#" -ne 2 ]
    then
      exit 9
    fi
    printf 'launched\n' >>"$2"
    sleep 3
    printf 'recovered check passed\n'
    ;;
  *)
    exit 10
    ;;
esac
