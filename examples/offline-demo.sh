#!/usr/bin/env bash
# Offline demo: serve the bundled capture and replay it with curl.
# Everything runs on 127.0.0.1; no external network is touched.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
SERVER_PID=""
cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

PORT="${1:-8099}"
BASE="http://127.0.0.1:$PORT"
fetch() { curl -s --noproxy '*' --max-time 5 "$@"; }

echo "# building harmock"
(cd "$ROOT" && go build -o "$WORKDIR/harmock" ./cmd/harmock)

echo "# serving examples/petstore.har on $BASE"
"$WORKDIR/harmock" serve "$ROOT/examples/petstore.har" --port "$PORT" --quiet &
SERVER_PID=$!
for _ in $(seq 1 50); do
  fetch "$BASE/__harmock__/health" >/dev/null 2>&1 && break
  sleep 0.1
done

echo
echo "# a recorded GET, replayed"
fetch "$BASE/api/pets?limit=2"; echo

echo
echo "# polling a job: sequential replay walks the recorded states"
for i in 1 2 3 4; do
  printf 'poll %d: ' "$i"
  fetch "$BASE/api/jobs/42"; echo
done

echo
echo "# rewinding with the admin endpoint"
fetch -X POST "$BASE/__harmock__/reset"; echo
printf 'poll after reset: '
fetch "$BASE/api/jobs/42"; echo

echo
echo "# body matching: the POST payload picks the right recording"
fetch -X POST -d '{"species":"cat","name":"Kuro"}' "$BASE/api/pets"; echo

echo
echo "# an unrecorded request gets a diagnosable 404"
fetch "$BASE/api/nothing"; echo

echo
echo "demo done — the entire session was served from one HAR file."
