#!/usr/bin/env bash
set -euo pipefail
binary=${1:?absolute binary path required}
smoke_dir=$(mktemp -d /tmp/mailpulse-smoke.XXXXXX)
daemon_pid=''
ntfy_pid=''
cleanup() {
  if [ -n "$daemon_pid" ]; then kill "$daemon_pid" 2>/dev/null || true; wait "$daemon_pid" 2>/dev/null || true; fi
  if [ -n "$ntfy_pid" ]; then kill "$ntfy_pid" 2>/dev/null || true; wait "$ntfy_pid" 2>/dev/null || true; fi
  rm -rf "$smoke_dir"
}
trap cleanup EXIT
export MAILPULSE_DATA_DIR="$smoke_dir/data"
export MAILPULSE_KEY_FILE="$smoke_dir/master.key"
export MAILPULSE_LISTEN=127.0.0.1:18089
python3 -u - "$smoke_dir" <<'PY' &
import json, pathlib, sys
from http.server import BaseHTTPRequestHandler, HTTPServer
root=pathlib.Path(sys.argv[1])
class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        data=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        assert data['topic']=='mailpulse-ci'
        assert data['message'] and data['title']
        (root/'delivered').write_text('ok')
        self.send_response(200); self.end_headers(); self.wfile.write(b'{"id":"fake"}')
    def log_message(self,*args): pass
server=HTTPServer(('127.0.0.1',0),Handler)
(root/'ntfy-port').write_text(str(server.server_port))
server.serve_forever()
PY
ntfy_pid=$!
"$binary" init
"$binary" serve >"$smoke_dir/daemon.log" 2>&1 &
daemon_pid=$!
ready=false
for _ in {1..100}; do
  if "$binary" status >"$smoke_dir/status.json" 2>/dev/null && [ -f "$smoke_dir/ntfy-port" ]; then ready=true; break; fi
  if ! kill -0 "$daemon_pid" 2>/dev/null; then cat "$smoke_dir/daemon.log"; exit 1; fi
  sleep 0.1
done
[ "$ready" = true ]
"$binary" healthcheck
"$binary" notify configure --url "http://127.0.0.1:$(cat "$smoke_dir/ntfy-port")" --topic mailpulse-ci --anonymous
"$binary" notify test
test -f "$smoke_dir/delivered"
"$binary" rule add --action ignore --domain example.test
"$binary" rule list >"$smoke_dir/rules.json"
python3 - "$smoke_dir" <<'PY'
import json,pathlib,sys
p=pathlib.Path(sys.argv[1]); rules=json.loads((p/'rules.json').read_text())
assert len(rules)==1 and rules[0]['sender_domain']=='example.test'
PY
"$binary" outbox list
kill -TERM "$daemon_pid"
wait "$daemon_pid"
daemon_pid=''
"$binary" serve >"$smoke_dir/daemon.log" 2>&1 &
daemon_pid=$!
for _ in {1..100}; do
  if "$binary" rule list >"$smoke_dir/rules.json" 2>/dev/null; then break; fi
  sleep 0.1
done
python3 - "$smoke_dir/rules.json" <<'PY'
import json,sys
assert len(json.load(open(sys.argv[1])))==1
PY
kill -TERM "$daemon_pid"
wait "$daemon_pid"
daemon_pid=''
echo 'Native daemon, private controls, ntfy publish and restart passed.'
