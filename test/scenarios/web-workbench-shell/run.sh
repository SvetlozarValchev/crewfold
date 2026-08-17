#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-web-shell.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/state/crewfold"
runtime_dir="$scenario_root/run/crewfold"
socket_path="$runtime_dir/crewfold.sock"
daemon_log="$scenario_root/daemon.log"
daemon_pid=""
chrome_pid=""

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'web workbench shell acceptance failed; diagnostics follow\n' >&2
    [ ! -f "$daemon_log" ] || tail -n 160 "$daemon_log" >&2
    [ ! -f "$scenario_root/browser.dom" ] || tail -n 80 "$scenario_root/browser.dom" >&2
  fi
  if [ -n "$daemon_pid" ] && kill -0 "$daemon_pid" 2>/dev/null
  then
    kill "$daemon_pid" 2>/dev/null || true
    wait "$daemon_pid" 2>/dev/null || true
  fi
  if [ -n "$chrome_pid" ] && kill -0 "$chrome_pid" 2>/dev/null
  then
    kill "$chrome_pid" 2>/dev/null || true
    wait "$chrome_pid" 2>/dev/null || true
  fi
  [ ! -d "$scenario_root" ] || find "$scenario_root" -depth -delete
}
trap cleanup EXIT HUP INT TERM

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -trimpath -o "$binary" "$repo_root/cmd/crewfold"
mkdir -m 0700 -p "$runtime_dir" "$scenario_root/fakebin"
mkdir -m 0700 "$scenario_root/world-engine-2"
git -C "$scenario_root/world-engine-2" init -q -b main
printf '# World Engine 2\n' >"$scenario_root/world-engine-2/README.md"
git -C "$scenario_root/world-engine-2" add README.md
git -C "$scenario_root/world-engine-2" -c user.name='Crewfold Fixture' -c user.email='fixture@invalid' commit -q -m 'initial fixture'

cat >"$scenario_root/fakebin/xdg-open" <<'SH'
#!/bin/sh
set -eu
test "$#" -eq 1
printf '%s\n' "$1" >"$CREWFOLD_OPEN_CAPTURE"
SH
chmod 0700 "$scenario_root/fakebin/xdg-open"

"$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" --web-address 127.0.0.1:0 2>"$daemon_log" &
daemon_pid=$!
attempts=0
until "$binary" status --socket "$socket_path" --output json >"$scenario_root/status.json" 2>/dev/null
do
  if ! kill -0 "$daemon_pid" 2>/dev/null
  then
    wait "$daemon_pid" || true
    daemon_pid=""
    exit 1
  fi
  attempts=$((attempts + 1))
  [ "$attempts" -lt 400 ] || exit 1
  sleep 0.01
done

export PATH="$scenario_root/fakebin:$PATH"
export XDG_STATE_HOME="$scenario_root/state"
export XDG_CONFIG_HOME="$scenario_root/config"
export XDG_RUNTIME_DIR="$scenario_root/run"
export CREWFOLD_OPEN_CAPTURE="$scenario_root/open.url"

"$binary" open >"$scenario_root/open.stdout" 2>"$scenario_root/open.stderr"
url=$(sed -n '1p' "$CREWFOLD_OPEN_CAPTURE")
origin=$(printf '%s' "$url" | sed 's|/#bootstrap=.*$||')
token=$(printf '%s' "$url" | sed -n 's|^http://127\.0\.0\.1:[0-9][0-9]*/#bootstrap=\([0-9a-f][0-9a-f]*\)$|\1|p')
test "${#token}" -eq 64
grep -Fq "Crewfold workbench opened at $origin" "$scenario_root/open.stdout"
if grep -Fq "$token" "$scenario_root/open.stdout" "$scenario_root/open.stderr" "$daemon_log"
then
  printf 'one-time bootstrap leaked to process output\n' >&2
  exit 1
fi

curl --silent --show-error --fail \
  -H "Origin: $origin" -H 'Content-Type: application/json' \
  -c "$scenario_root/cookies" \
  --data "{\"bootstrap\":\"$token\"}" \
  "$origin/api/v1/session" >"$scenario_root/session.json"
grep -Fq '"schema":"urn:crewfold:schema:web:workbench-session:v1"' "$scenario_root/session.json"
grep -Fq '"status":"authenticated"' "$scenario_root/session.json"
api_base=$(sed -n 's/.*"api_base":"\([^"]*\)".*/\1/p' "$scenario_root/session.json")
test -n "$api_base"

curl --silent --show-error --fail -b "$scenario_root/cookies" \
  "$origin$api_base/status" >"$scenario_root/web-status.json"
grep -Fq '"schema":"urn:crewfold:schema:web:workbench-status:v1"' "$scenario_root/web-status.json"
grep -Fq '"status":"ok"' "$scenario_root/web-status.json"

replay_status=$(curl --silent --output "$scenario_root/replay.json" --write-out '%{http_code}' \
  -H "Origin: $origin" -H 'Content-Type: application/json' \
  --data "{\"bootstrap\":\"$token\"}" "$origin/api/v1/session")
test "$replay_status" -eq 401

host_status=$(curl --silent --output "$scenario_root/host.json" --write-out '%{http_code}' \
  -H 'Host: localhost:1' "$origin/")
test "$host_status" -eq 421

# A new one-time grant must complete the same exchange in a real browser. The
# rendered DOM proves that the embedded React client reached authenticated live
# daemon state rather than merely serving static HTML.
export CREWFOLD_OPEN_CAPTURE="$scenario_root/browser.url"
"$binary" open >"$scenario_root/browser-open.stdout" 2>"$scenario_root/browser-open.stderr"
browser_url=$(sed -n '1p' "$CREWFOLD_OPEN_CAPTURE")
google-chrome --headless --disable-gpu --no-sandbox --disable-dev-shm-usage \
  --user-data-dir="$scenario_root/chrome" --virtual-time-budget=2000 \
  --dump-dom "$browser_url" >"$scenario_root/browser.dom" 2>"$scenario_root/browser.stderr"
grep -Fq 'Bring your repository into the workbench.' "$scenario_root/browser.dom"
grep -Fq 'Set up your workbench' "$scenario_root/browser.dom"
grep -Fq 'Codex subscription' "$scenario_root/browser.dom"
grep -Fq 'lucide' "$scenario_root/browser.dom"

# Drive the built application through a real browser from empty canonical
# state. All domain setup and execution below is performed by browser controls;
# the shell supplies only an isolated committed repository and opens Crewfold.
export CREWFOLD_OPEN_CAPTURE="$scenario_root/flow.url"
"$binary" open >"$scenario_root/flow-open.stdout" 2>"$scenario_root/flow-open.stderr"
flow_url=$(sed -n '1p' "$CREWFOLD_OPEN_CAPTURE")
flow_profile="$scenario_root/chrome-flow"
google-chrome --headless --disable-gpu --no-sandbox --disable-dev-shm-usage \
  --window-size=1440,1000 --user-data-dir="$flow_profile" --remote-debugging-port=0 "$flow_url" \
  >"$scenario_root/flow-chrome.stdout" 2>"$scenario_root/flow-chrome.stderr" &
chrome_pid=$!
attempts=0
until [ -s "$flow_profile/DevToolsActivePort" ]
do
  kill -0 "$chrome_pid" 2>/dev/null || exit 1
  attempts=$((attempts + 1))
  [ "$attempts" -lt 400 ] || exit 1
  sleep 0.01
done
debugger_port=$(sed -n '1p' "$flow_profile/DevToolsActivePort")
node "$repo_root/test/scenarios/web-workbench-shell/browser.mjs" \
  "$debugger_port" "$scenario_root/world-engine-2" "$scenario_root/browser-flow.json"
grep -Fq '"iconCount"' "$scenario_root/browser-flow.json"
grep -Fq '"project-briefing"' "$scenario_root/browser-flow.json"
kill "$chrome_pid" 2>/dev/null || true
wait "$chrome_pid" 2>/dev/null || true
chrome_pid=""

"$binary" daemon stop --socket "$socket_path" --output json >"$scenario_root/stop.json"
wait "$daemon_pid"
daemon_pid=""

printf 'Authenticated local web workbench browser acceptance: PASS\n'
