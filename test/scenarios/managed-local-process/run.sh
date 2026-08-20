#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)
go_runner="$repo_root/scripts/go.sh"
scenario_root=$(mktemp -d "${TMPDIR:-/tmp}/crewfold-managed-process.XXXXXX")
binary="$scenario_root/crewfold"
data_dir="$scenario_root/data"
socket_path="$scenario_root/crewfold.sock"
checkout_root="$scenario_root/generic-process"
daemon_log="$scenario_root/daemon.log"
daemon_pid=""
chrome_pid=""

cleanup() {
  status=$?
  if [ "$status" -ne 0 ]
  then
    printf 'managed local process acceptance failed; diagnostics follow\n' >&2
    [ ! -f "$daemon_log" ] || tail -n 160 "$daemon_log" >&2
    for path in "$scenario_root"/*.json "$scenario_root"/*.txt
    do
      [ ! -f "$path" ] || {
        printf '\n== %s ==\n' "$path" >&2
        tail -n 80 "$path" >&2
      }
    done
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

extract_id() {
  prefix=$1
  path=$2
  sed -n "s/.*\"id\":\"\(${prefix}_[0-9a-f]*\)\".*/\1/p" "$path" | sed -n '1p'
}

wait_for_text() {
  wanted=$1
  output=$2
  shift 2
  attempts=0
  while :
  do
    if "$@" >"$output" 2>/dev/null && grep -Fq "$wanted" "$output"
    then
      return
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 600 ]
    then
      printf 'timed out waiting for %s\n' "$wanted" >&2
      return 1
    fi
    sleep 0.01
  done
}

GOTOOLCHAIN=local GOPROXY=off "$go_runner" build -trimpath -o "$binary" "$repo_root/cmd/crewfold"
mkdir -m 0700 -p "$checkout_root" "$scenario_root/fakebin"
git -C "$checkout_root" init -q -b main
cat >"$checkout_root/server.py" <<'PY'
import http.server
import sys

port = int(sys.argv[1])

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"generic managed process\n"
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        print("fixture-http " + (format % args), flush=True)

print(f"fixture-ready {port}", flush=True)
http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
PY
git -C "$checkout_root" add server.py
git -C "$checkout_root" -c user.name='Crewfold Fixture' -c user.email='fixture@invalid' commit -q -m 'generic process fixture'

port=$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)
python_executable=$(command -v python3)
cat >"$scenario_root/fakebin/xdg-open" <<'SH'
#!/bin/sh
set -eu
test "$#" -eq 1
printf '%s\n' "$1" >"$CREWFOLD_OPEN_CAPTURE"
SH
chmod 0700 "$scenario_root/fakebin/xdg-open"

"$binary" daemon run --data-dir "$data_dir" --socket "$socket_path" --web-address 127.0.0.1:0 2>"$daemon_log" &
daemon_pid=$!
wait_for_text '"status":"ok"' "$scenario_root/status.json" \
  "$binary" status --socket "$socket_path" --output json

export PATH="$scenario_root/fakebin:$PATH"
export CREWFOLD_OPEN_CAPTURE="$scenario_root/setup.url"
"$binary" open --socket "$socket_path" >"$scenario_root/setup-open.txt"
setup_url=$(sed -n '1p' "$CREWFOLD_OPEN_CAPTURE")
google-chrome --headless --disable-gpu --no-sandbox --disable-dev-shm-usage \
  --window-size=1280,900 --user-data-dir="$scenario_root/setup-chrome" --remote-debugging-port=0 \
  "$setup_url" >"$scenario_root/setup-browser.stdout" 2>"$scenario_root/setup-browser.stderr" &
chrome_pid=$!
attempts=0
until [ -s "$scenario_root/setup-chrome/DevToolsActivePort" ]
do
  kill -0 "$chrome_pid" 2>/dev/null || exit 1
  attempts=$((attempts + 1))
  [ "$attempts" -lt 400 ] || exit 1
  sleep 0.01
done
debugger_port=$(sed -n '1p' "$scenario_root/setup-chrome/DevToolsActivePort")
node "$repo_root/test/scenarios/managed-local-process/browser-m24.mjs" "$debugger_port" setup "$checkout_root" "$scenario_root/setup-browser.json"
grep -Fq '"setup": true' "$scenario_root/setup-browser.json"
kill "$chrome_pid" 2>/dev/null || true
wait "$chrome_pid" 2>/dev/null || true
chrome_pid=""

"$binary" project inspect generic-process --workspace personal --socket "$socket_path" --output json >"$scenario_root/project.json"
checkout_id=$(extract_id co "$scenario_root/project.json")
test -n "$checkout_id"

"$binary" process define fixture-http \
  --workspace personal --project generic-process --checkout "$checkout_id" \
  --description 'Generic Python HTTP fixture' \
  --executable "$python_executable" --arg=-u --arg=server.py --arg="$port" \
  --network loopback --health http --health-host 127.0.0.1 --health-port "$port" --health-path / \
  --health-interval 100ms --health-timeout 100ms --restart never --max-restarts 2 --stop-grace 1s \
  --socket "$socket_path" --idempotency-key managed-process-definition --output json >"$scenario_root/definition.json"
definition_id=$(extract_id svcdef "$scenario_root/definition.json")
test -n "$definition_id"

"$binary" process start "$definition_id" --workspace personal --expected-revision 1 \
  --socket "$socket_path" --idempotency-key managed-process-start --output json >"$scenario_root/start.json"
instance_id=$(extract_id svcinst "$scenario_root/start.json")
test -n "$instance_id"

wait_for_text '"status":"healthy"' "$scenario_root/healthy.json" \
  "$binary" process show "$instance_id" --workspace personal --socket "$socket_path" --output json
curl --silent --show-error --fail "http://127.0.0.1:$port/" >"$scenario_root/http.txt"
grep -Fxq 'generic managed process' "$scenario_root/http.txt"
"$binary" process logs "$instance_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/live-logs.json"
grep -Fq 'fixture-ready' "$scenario_root/live-logs.json"

revision=$(sed -n 's/.*"revision":\([0-9][0-9]*\).*/\1/p' "$scenario_root/healthy.json" | sed -n '1p')
test -n "$revision"
"$binary" process restart "$instance_id" --workspace personal --expected-revision "$revision" \
  --socket "$socket_path" --idempotency-key managed-process-restart --output json >"$scenario_root/restart.json"
wait_for_text '"status":"healthy"' "$scenario_root/restarted.json" \
  "$binary" process show "$instance_id" --workspace personal --socket "$socket_path" --output json
grep -Eq '"restart_count":[1-9][0-9]*' "$scenario_root/restarted.json"
curl --silent --show-error --fail "http://127.0.0.1:$port/" >"$scenario_root/restarted-http.txt"

export CREWFOLD_OPEN_CAPTURE="$scenario_root/browser.url"
"$binary" open --socket "$socket_path" >"$scenario_root/open.txt"
browser_url=$(sed -n '1p' "$CREWFOLD_OPEN_CAPTURE")
test -n "$browser_url"
google-chrome --headless --disable-gpu --no-sandbox --disable-dev-shm-usage \
  --window-size=1280,900 --user-data-dir="$scenario_root/chrome" --remote-debugging-port=0 \
  "$browser_url" >"$scenario_root/browser.stdout" 2>"$scenario_root/browser.stderr" &
chrome_pid=$!
attempts=0
until [ -s "$scenario_root/chrome/DevToolsActivePort" ]
do
  kill -0 "$chrome_pid" 2>/dev/null || exit 1
  attempts=$((attempts + 1))
  [ "$attempts" -lt 400 ] || exit 1
  sleep 0.01
done
debugger_port=$(sed -n '1p' "$scenario_root/chrome/DevToolsActivePort")
node "$repo_root/test/scenarios/managed-local-process/browser-m24.mjs" "$debugger_port" inspect "$checkout_root" "$scenario_root/browser.json"
grep -Fq '"logsVisible": true' "$scenario_root/browser.json"
kill "$chrome_pid" 2>/dev/null || true
wait "$chrome_pid" 2>/dev/null || true
chrome_pid=""

revision=$(sed -n 's/.*"revision":\([0-9][0-9]*\).*/\1/p' "$scenario_root/restarted.json" | sed -n '1p')
"$binary" process stop "$instance_id" --workspace personal --expected-revision "$revision" \
  --socket "$socket_path" --idempotency-key managed-process-stop --output json >"$scenario_root/stop-request.json"
wait_for_text '"status":"stopped"' "$scenario_root/stopped.json" \
  "$binary" process show "$instance_id" --workspace personal --socket "$socket_path" --output json
"$binary" process logs "$instance_id" --workspace personal --socket "$socket_path" --output json >"$scenario_root/terminal-logs.json"
grep -Fq 'fixture-ready' "$scenario_root/terminal-logs.json"
if curl --silent --connect-timeout 1 --fail "http://127.0.0.1:$port/" >/dev/null 2>&1
then
  printf 'managed process remained reachable after terminal stop\n' >&2
  exit 1
fi

"$binary" daemon stop --socket "$socket_path" --output json >"$scenario_root/daemon-stop.json"
wait "$daemon_pid"
daemon_pid=""

printf 'Generic managed local process black-box acceptance: PASS\n'
