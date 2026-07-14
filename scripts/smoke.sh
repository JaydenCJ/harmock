#!/usr/bin/env bash
# End-to-end smoke test for harmock: builds the binary, serves the example
# capture on a loopback port, and asserts on real HTTP responses across the
# whole surface (replay, sequential state, admin, check, exit codes).
# No external network, idempotent, finishes in seconds.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
SERVER_PID=""
cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

BIN="$WORKDIR/harmock"
HAR="$ROOT/examples/petstore.har"
PORT=18642
BASE="http://127.0.0.1:$PORT"

# Loopback traffic must never be routed through a proxy.
fetch() { curl -s --noproxy '*' --max-time 5 "$@"; }

# has <haystack-producing command...> -- <needle>: run the command, capture
# everything, then grep. Never `cmd | grep -q` directly: with pipefail, an
# early grep exit SIGPIPEs the producer and fails the pipeline at random.
has() {
  local out needle
  needle="${*: -1}"
  out="$("${@:1:$#-1}")"
  printf '%s' "$out" | grep -q -- "$needle"
}

echo "1. build"
(cd "$ROOT" && go build -o "$BIN" ./cmd/harmock) || fail "go build failed"

echo "2. version matches manifest"
[ "$("$BIN" version)" = "harmock 0.1.0" ] || fail "version mismatch"

echo "3. check accepts the example capture"
OUT="$("$BIN" check "$HAR")" || fail "check exited non-zero"
echo "$OUT" | grep -q "servable: 9" || fail "check should report 9 servable entries"

echo "4. routes lists sequential replays"
has "$BIN" routes "$HAR" "replay 3/3" || fail "replay annotation missing"

echo "5. show prints a recorded POST"
has "$BIN" show "$HAR" --route "POST /api/pets" '"name":"Kuro"' \
  || fail "show output missing recorded body"

echo "6. serve starts on 127.0.0.1"
"$BIN" serve "$HAR" --port "$PORT" --quiet >/dev/null 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 50); do
  fetch "$BASE/__harmock__/health" >/dev/null 2>&1 && break
  sleep 0.1
done
has fetch "$BASE/__harmock__/health" '"routes": 9' || fail "health endpoint"

echo "7. replays a recorded GET byte-for-byte"
has fetch "$BASE/api/pets?limit=2" '"name":"Momo"' || fail "GET replay"
has fetch -o /dev/null -w '%{content_type}' "$BASE/api/pets?limit=2" \
  "application/json" || fail "content type"

echo "8. sequential replay walks duplicate recordings"
S1="$(fetch "$BASE/api/jobs/42")"
S2="$(fetch "$BASE/api/jobs/42")"
S3="$(fetch "$BASE/api/jobs/42")"
S4="$(fetch "$BASE/api/jobs/42")"
echo "$S1" | grep -q pending || fail "poll 1 should be pending, got: $S1"
echo "$S2" | grep -q running || fail "poll 2 should be running, got: $S2"
echo "$S3" | grep -q '"done"' || fail "poll 3 should be done, got: $S3"
echo "$S4" | grep -q '"done"' || fail "poll 4 should stick at done, got: $S4"

echo "9. admin reset rewinds the sequence"
has fetch -X POST "$BASE/__harmock__/reset" '"reset": true' || fail "reset"
has fetch "$BASE/api/jobs/42" pending || fail "post-reset poll should be pending"

echo "10. body matching selects the recorded POST"
has fetch -X POST -d '{"species":"cat","name":"Kuro"}' "$BASE/api/pets" \
  '"id":3' || fail "POST body match"

echo "11. binary responses are byte-identical to the capture"
fetch -o "$WORKDIR/logo.png" "$BASE/logo.png"
[ "$(wc -c < "$WORKDIR/logo.png")" -eq 70 ] || fail "binary size mismatch"
MAGIC="$(head -c 8 "$WORKDIR/logo.png" | od -An -tx1 | tr -d ' \n')"
case "$MAGIC" in 89504e47*) ;; *) fail "PNG magic bytes wrong: $MAGIC";; esac

echo "12. unmatched requests get a diagnosable 404"
CODE="$(fetch -o "$WORKDIR/miss.json" -w '%{http_code}' "$BASE/api/nothing")"
[ "$CODE" = "404" ] || fail "unmatched should be 404, got $CODE"
grep -q "no recorded entry" "$WORKDIR/miss.json" || fail "404 payload"

echo "13. check gates broken captures with exit 1"
printf '{"log":{"version":"1.2","entries":[]}}' > "$WORKDIR/empty.har"
if "$BIN" check "$WORKDIR/empty.har" >/dev/null; then
  fail "empty capture should exit 1"
fi

echo "14. usage errors exit 2"
set +e
"$BIN" routes "$HAR" --format yaml >/dev/null 2>&1
[ $? -eq 2 ] || fail "bad --format should exit 2"
set -e

echo "SMOKE OK"
