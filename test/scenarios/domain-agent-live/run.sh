#!/bin/sh
set -eu

if [ "${CREWFOLD_RUN_LIVE_CODEX:-}" != "1" ]
then
  printf 'SKIP: set CREWFOLD_RUN_LIVE_CODEX=1 to run the subscription-backed M22 browser canary\n'
  exit 0
fi

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-domain-live.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/state/crewfold"
runtime_dir="$scenario_root/run/crewfold"
socket_path="$runtime_dir/crewfold.sock"
daemon_log="$scenario_root/daemon.log"
daemon_pid=""
chrome_pid=""
herdr_pid=""

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'M22 live domain-agent acceptance failed; diagnostics follow\n' >&2
    [ ! -f "$daemon_log" ] || tail -n 200 "$daemon_log" >&2
    [ ! -f "$scenario_root/browser-result.json" ] || sed -n '1,240p' "$scenario_root/browser-result.json" >&2
    if [ -n "${CREWFOLD_SCREENSHOT_DIR:-}" ]
    then
      mkdir -p "$CREWFOLD_SCREENSHOT_DIR"
      [ ! -f "$daemon_log" ] || cp "$daemon_log" "$CREWFOLD_SCREENSHOT_DIR/daemon.log"
      [ ! -f "$scenario_root/herdr.stderr" ] || cp "$scenario_root/herdr.stderr" "$CREWFOLD_SCREENSHOT_DIR/herdr.stderr"
      [ ! -f "$scenario_root/chrome.stderr" ] || cp "$scenario_root/chrome.stderr" "$CREWFOLD_SCREENSHOT_DIR/chrome.stderr"
    fi
  fi
  if [ -n "$chrome_pid" ] && kill -0 "$chrome_pid" 2>/dev/null
  then
    kill "$chrome_pid" 2>/dev/null || true
    wait "$chrome_pid" 2>/dev/null || true
  fi
  if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    kill "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  if [ -n "$herdr_pid" ] && kill -0 "$herdr_pid" 2>/dev/null
  then
    kill "$herdr_pid" 2>/dev/null || true
    wait "$herdr_pid" 2>/dev/null || true
  fi
  [ ! -d "$scenario_root" ] || find "$scenario_root" -depth -delete
}
trap cleanup EXIT HUP INT TERM

command -v codex >/dev/null 2>&1 || { printf 'Codex is unavailable\n' >&2; exit 1; }
command -v google-chrome >/dev/null 2>&1 || { printf 'Google Chrome is unavailable\n' >&2; exit 1; }

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -trimpath -o "$binary" "$repo_root/cmd/crewfold"
mkdir -m 0700 -p "$runtime_dir" "$scenario_root/fakebin" "$scenario_root/domain-repository" "$scenario_root/config/herdr"
git -C "$scenario_root/domain-repository" init -q -b main
printf '# M22 live domain\n\nThis repository exists only for the durable-agent subscription canary.\n' >"$scenario_root/domain-repository/README.md"
git -C "$scenario_root/domain-repository" add README.md
git -C "$scenario_root/domain-repository" -c user.name='Crewfold Live Fixture' -c user.email='live@invalid' commit -q -m 'initial fixture'

printf '%s\n' '#!/bin/sh' 'set -eu' 'printf "%s\\n" "$1" >"$CREWFOLD_OPEN_CAPTURE"' >"$scenario_root/fakebin/xdg-open"
chmod 0700 "$scenario_root/fakebin/xdg-open"

XDG_CONFIG_HOME="$scenario_root/config" HERDR_CONFIG_PATH="$scenario_root/config/herdr/config.toml" \
  herdr server >"$scenario_root/herdr.stdout" 2>"$scenario_root/herdr.stderr" &
herdr_pid=$!
attempts=0
until [ -S "$scenario_root/config/herdr/herdr.sock" ]
do
  kill -0 "$herdr_pid" 2>/dev/null || exit 1
  attempts=$((attempts + 1))
  [ "$attempts" -lt 500 ] || exit 1
  sleep 0.02
done

PATH="$scenario_root/fakebin:$PATH" \
XDG_STATE_HOME="$scenario_root/state" XDG_CONFIG_HOME="$scenario_root/config" XDG_RUNTIME_DIR="$scenario_root/run" \
"$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" --web-address 127.0.0.1:0 \
  --codex-binary "$(command -v codex)" --codex-tool-network-access true 2>"$daemon_log" &
daemon_pid=$!
attempts=0
until "$binary" status --socket "$socket_path" --output json >/dev/null 2>&1
do
  kill -0 "$daemon_pid" 2>/dev/null || exit 1
  attempts=$((attempts + 1))
  [ "$attempts" -lt 500 ] || exit 1
  sleep 0.02
done

export PATH="$scenario_root/fakebin:$PATH"
export XDG_STATE_HOME="$scenario_root/state"
export XDG_CONFIG_HOME="$scenario_root/config"
export XDG_RUNTIME_DIR="$scenario_root/run"
export CREWFOLD_OPEN_CAPTURE="$scenario_root/open.url"
"$binary" open >/dev/null
flow_url=$(sed -n '1p' "$CREWFOLD_OPEN_CAPTURE")
profile="$scenario_root/chrome"
google-chrome --headless --disable-gpu --no-sandbox --disable-dev-shm-usage \
  --window-size=1440,1000 --user-data-dir="$profile" --remote-debugging-port=0 "$flow_url" \
  >"$scenario_root/chrome.stdout" 2>"$scenario_root/chrome.stderr" &
chrome_pid=$!
attempts=0
until [ -s "$profile/DevToolsActivePort" ]
do
  kill -0 "$chrome_pid" 2>/dev/null || exit 1
  attempts=$((attempts + 1))
  [ "$attempts" -lt 500 ] || exit 1
  sleep 0.02
done
debugger_port=$(sed -n '1p' "$profile/DevToolsActivePort")
CREWFOLD_SCREENSHOT_DIR="${CREWFOLD_SCREENSHOT_DIR:-$scenario_root/screenshots}" \
  node "$repo_root/test/scenarios/domain-agent-live/browser-live.mjs" \
  "$debugger_port" "$scenario_root/domain-repository" "$scenario_root/browser-result.json"

grep -Fq '"orchidReply": true' "$scenario_root/browser-result.json"
grep -Fq '"fernReply": true' "$scenario_root/browser-result.json"
grep -Fq '"childVisible": true' "$scenario_root/browser-result.json"
grep -Fq '"proposalAccepted": true' "$scenario_root/browser-result.json"
grep -Fq '"workerCompleted": true' "$scenario_root/browser-result.json"
grep -Fq '"providerLocalHelper": false' "$scenario_root/browser-result.json"
grep -Fq '"browserExceptions": []' "$scenario_root/browser-result.json"
test -z "$(git -C "$scenario_root/domain-repository" status --porcelain=v1)"

"$binary" daemon stop --socket "$socket_path" --output json >/dev/null
wait "$daemon_pid"
daemon_pid=""
kill "$chrome_pid" 2>/dev/null || true
wait "$chrome_pid" 2>/dev/null || true
chrome_pid=""

printf 'Subscription-backed M22 durable-agent browser acceptance: PASS\n'
