#!/usr/bin/env bash
# End-to-end acceptance: normal loopback link, illegal-candidate rejection,
# parameter-error exit code, and internal self-check.
set -euo pipefail
cd "$(dirname "$0")/.."

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/relaycheck"
go build -o "$BIN" ./cmd/relaycheck

fail() { echo "demo: FAIL: $*" >&2; exit 1; }

echo "== case 1: normal link (expect exit 0, 1200 forwarded packets) =="
out="$("$BIN" run --packets 1200 --payload-bytes 960 --interval 2ms --bind 127.0.0.1 --json)" || fail "run exited $?"
echo "$out"
echo "$out" | grep -q '"forwarded_packets": 1200' || fail "forwarded_packets != 1200"
echo "$out" | grep -q '"dropped_packets": 0' || fail "dropped_packets != 0"
echo "$out" | grep -q '"duplicate_packets": 0' || fail "duplicate_packets != 0"
pairs="$(echo "$out" | sed -n 's/.*"candidate_pairs": \([0-9]*\).*/\1/p')"
[ "$pairs" -ge 1 ] && [ "$pairs" -le 4 ] || fail "candidate_pairs=$pairs out of [1,4]"

echo "== case 2: illegal candidate address (expect exit 3, rejected_candidates=1) =="
code=0
out="$("$BIN" run --packets 50 --inject-candidate 203.0.113.7:5000 --json 2>&1)" || code=$?
echo "$out"
[ "$code" -eq 3 ] || fail "expected exit 3, got $code"
echo "$out" | grep -q '"rejected_candidates": 1' || fail "rejected_candidates != 1"

echo "== case 3: parameter error (expect exit 2) =="
code=0
"$BIN" run --packets 0 2>"$TMP/err.txt" || code=$?
[ "$code" -eq 2 ] || fail "expected exit 2, got $code"
grep -q 'packets:' "$TMP/err.txt" || fail "missing field-level error"

echo "== case 4: self-check (expect exit 0) =="
"$BIN" self-check || fail "self-check exited $?"

echo "demo: all cases passed"
