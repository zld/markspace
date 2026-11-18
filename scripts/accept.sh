#!/usr/bin/env bash
#
# accept.sh — reproducible acceptance run for relaylab.
#
# Builds the binary into /tmp (never into the delivery tree), runs the exact
# acceptance experiment, then validates the JSON report: required fields,
# candidate-pair range, packet counts and magnitudes, non-negative jitter and
# a clean shutdown. Exit status mirrors the documented CLI contract:
#   0 success | 2 argument/signaling | 3 transport/timeout | 4 shutdown timeout
#
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="/tmp/relaylab"
REPORT="/tmp/relaylab-report.json"
DURATION="${DURATION:-5s}"
PACKETS="${PACKETS:-1200}"
QUEUE="${QUEUE:-64}"

echo "==> toolchain"
go version

echo "==> dependencies"
(cd "$ROOT_DIR" && go mod download)

echo "==> build -> $BIN"
(cd "$ROOT_DIR" && go build -o "$BIN" ./cmd/relaylab)

echo "==> unit + race tests"
(cd "$ROOT_DIR" && go test -race -timeout 90s ./...)

echo "==> acceptance run (duration=$DURATION packets=$PACKETS queue=$QUEUE)"
set +e
"$BIN" run \
  --duration "$DURATION" \
  --packets "$PACKETS" \
  --queue "$QUEUE" \
  --overflow drop \
  --seed 42 \
  --json >"$REPORT"
RC=$?
set -e
cat "$REPORT"
echo "==> relaylab exit code: $RC"

if [ "$RC" -ne 0 ]; then
  echo "FAIL: experiment exited with $RC (expected 0)" >&2
  exit "$RC"
fi

echo "==> validating report fields"
fail() { echo "FAIL: $*" >&2; exit 1; }

# jq is available on macOS via the Go-independent toolchain check below; fall
# back to a tiny python3 verifier if jq is absent.
if command -v jq >/dev/null 2>&1; then
  get() { jq -r "$1" "$REPORT"; }
else
  get() { python3 -I -c 'import json,sys; print(json.load(open(sys.argv[2])).get(sys.argv[1],""))' "$1" "$REPORT"; }
fi

PAIRS="$(get '.candidate_pairs | length')"
[ "$PAIRS" -ge 1 ] && [ "$PAIRS" -le 4 ] || fail "candidate_pairs=$PAIRS outside [1,4]"

SENT="$(get '.sent_packets')"
[ "$SENT" -gt 0 ] || fail "sent_packets must be positive"
# Within an order of magnitude of the requested count.
[ "$SENT" -ge $((PACKETS / 2)) ] && [ "$SENT" -le $((PACKETS * 2)) ] || fail "sent_packets=$SENT magnitude wrong"

FWD="$(get '.forwarded_packets')"
[ "$FWD" -gt 0 ] || fail "forwarded_packets must be positive"

for field in dropped_packets duplicate_packets reordered_packets; do
  val="$(get ".$field")"
  [ "$val" -ge 0 ] || fail "$field must be non-negative"
done

# drop mode with backpressure must actually exercise the overflow path.
DROP="$(get '.dropped_packets')"
[ "$DROP" -gt 0 ] || fail "dropped_packets must be positive in drop/backpressure mode"

JTICKS="$(get '.jitter_rtp_ticks')"
[ "$JTICKS" -ge 0 ] || fail "jitter_rtp_ticks must be non-negative integer"

JMS="$(get '.jitter_ms')"
# Non-negative numeric (may be float).
python3 -I -c 'import json,sys; sys.exit(0 if json.load(open(sys.argv[1]))["jitter_ms"] >= 0 else 1)' "$REPORT" \
  || fail "jitter_ms must be non-negative"

CLEAN="$(get '.shutdown_clean')"
[ "$CLEAN" = "true" ] || fail "shutdown_clean must be true"

# Security: no non-loopback IPv4 address may appear anywhere in the report.
# Extract every dotted-quad and fail if any is not within 127.0.0.0/8.
bad_ip="$(grep -Eo '([0-9]{1,3}\.){3}[0-9]{1,3}' "$REPORT" | grep -v '^127\.' || true)"
if [ -n "$bad_ip" ]; then
  echo "FAIL: non-loopback address found: $bad_ip" >&2
  exit 1
fi
if grep -Eiq '\.local|192\.168\.|10\.[0-9]|172\.(1[6-9]|2[0-9]|3[01])\.|169\.254\.|::1|fe80' "$REPORT"; then
  fail "external/mDNS/IPv6 address found in report"
fi

echo "PASS: all acceptance checks satisfied (pairs=$PAIRS sent=$SENT forwarded=$FWD dropped=$DROP jitter=${JTICKS}ticks)"
exit 0
