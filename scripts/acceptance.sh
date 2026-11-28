#!/bin/bash
# Acceptance script for the local media kernel. Runs the full verification
# sequence against a freshly built binary so exit codes are observable
# (go run collapses child exit codes to 1).
set -uo pipefail
cd "$(dirname "$0")/.."
BIN=.tmp/mediademo
DEMO=.tmp/demo
fail() { echo "ACCEPTANCE FAIL: $*" >&2; exit 1; }

echo "== build & test =="
go mod tidy || fail "go mod tidy"
go build ./... || fail "go build"
go test ./... || fail "go test"
mkdir -p .tmp
go build -o "$BIN" ./cmd/mediademo || fail "build mediademo"

echo "== generate =="
rm -rf "$DEMO"
"$BIN" generate --dir "$DEMO" || fail "generate"

echo "== verify (good + trace + range) =="
"$BIN" verify --input "$DEMO/good.mp4" --trace "$DEMO/reordered.rtp" --range 2/1:5/1 || fail "verify good"

echo "== determinism =="
S1=$("$BIN" verify --input "$DEMO/good.mp4" --range 2/1:5/1 | grep -o 'sha256=[0-9a-f]*')
S2=$("$BIN" verify --input "$DEMO/good.mp4" --range 2/1:5/1 | grep -o 'sha256=[0-9a-f]*')
[ "$S1" = "$S2" ] || fail "checksums differ across runs: $S1 vs $S2"
echo "stable $S1"

echo "== corrupt path (expect exit 2) =="
"$BIN" verify --input "$DEMO/truncated.mp4"
rc=$?
[ $rc -eq 2 ] || fail "truncated input exit=$rc want 2"

echo "== argument error (expect exit 1) =="
"$BIN" verify >/dev/null 2>&1
rc=$?
[ $rc -eq 1 ] || fail "missing args exit=$rc want 1"
"$BIN" verify --input "$DEMO/good.mp4" --range bogus >/dev/null 2>&1
rc=$?
[ $rc -eq 1 ] || fail "bad range exit=$rc want 1"

echo "== interrupt + resume =="
"$BIN" resume --dir "$DEMO/interrupted" --interrupt-after 3 || fail "interrupt run"
OUT=$("$BIN" resume --dir "$DEMO/interrupted") || fail "resume run"
echo "$OUT"
RMS=$(echo "$OUT" | grep -o 'resume_ms=[0-9]*' | cut -d= -f2)
FMS=$(echo "$OUT" | grep -o 'full_ms=[0-9]*' | cut -d= -f2)
[ "$RMS" -lt "$FMS" ] || fail "resume ${RMS}ms not faster than full ${FMS}ms"

echo "ACCEPTANCE OK"
