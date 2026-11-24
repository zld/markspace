#!/usr/bin/env bash
# End-to-end acceptance: generate samples, exercise run/resume/corrupt paths
# and assert the documented exit codes and deterministic digests.
# Usage: ./scripts/acceptance.sh
set -euo pipefail

cd "$(dirname "$0")/.."

BIN="$(mktemp -d)/mediapack"
DEMO_OUT="$(mktemp -d)/demo"
go build -o "$BIN" ./cmd/mediapack

echo "== demo generation (expect exit 0) =="
"$BIN" demo --seed 7 --out "$DEMO_OUT" >/dev/null
echo "demo exit=$?"

echo "== run truncated input (expect exit 2) =="
set +e
"$BIN" run --input "$DEMO_OUT/truncated.webm" --index "$(mktemp -d)/idx" 2>/tmp/mp-err2
code2=$?
set -e
echo "truncated exit=$code2"
test "$code2" -eq 2
grep -q "truncated" /tmp/mp-err2

echo "== interrupt then resume (expect resume block 3) =="
IDX="$(mktemp -d)/idx"
"$BIN" run --input "$DEMO_OUT/valid.webm" --index "$IDX" --interrupt-after 3 >/tmp/mp-r1
"$BIN" run --input "$DEMO_OUT/valid.webm" --index "$IDX" >/tmp/mp-r2
grep -q "resume_block=3" /tmp/mp-r2
grep -q "resume_elapsed_ms=" /tmp/mp-r2
grep -q "payload_sha256=" /tmp/mp-r2
echo "resume verified"

echo "== repeated runs must hash identically =="
h1=$("$BIN" run --input "$DEMO_OUT/valid.webm" --index "$(mktemp -d)/a" | grep payload_sha256)
h2=$("$BIN" run --input "$DEMO_OUT/valid.webm" --index "$(mktemp -d)/b" | grep payload_sha256)
test "$h1" = "$h2"
echo "digest stable: $h1"

echo "== corrupt index (expect exit 3 with checkpoint offset) =="
CIDX="$(mktemp -d)/cidx"
"$BIN" run --input "$DEMO_OUT/valid.webm" --index "$CIDX" --interrupt-after 3 >/dev/null
set +e
"$BIN" run --input "$DEMO_OUT/valid.webm" --index "$CIDX" --corrupt-index 2>/tmp/mp-err3
code3=$?
set -e
echo "corrupt-index exit=$code3"
test "$code3" -eq 3
grep -q "checkpoint offset" /tmp/mp-err3

echo "ACCEPTANCE PASSED"
