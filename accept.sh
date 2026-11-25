#!/usr/bin/env bash
#
# accept.sh - reproducible offline acceptance run for the media kernel.
# Runs entirely locally on macOS with no external services. All output is
# machine-readable JSON; exit codes follow the documented contract:
#   0 spec passed | 1 business validation failure | 2 argument/runtime error
#
set -u

cd "$(dirname "$0")"
STATE=".assetcheck-state"
BIN="$(mktemp -d)/assetcheck"
fail=0

pass() { printf 'PASS: %s\n' "$1"; }
note() { printf '\n--- %s ---\n' "$1"; }

note "build"
go build -o "$BIN" ./cmd/assetcheck || { echo "build failed"; exit 2; }
pass "go build ./cmd/assetcheck"

note "full module test suite"
go test ./... || fail=1
pass "go test ./..."

rm -rf "$STATE"

note "1) clean demo (expect exit 0, packets>0, losses+dups, peak<=cap, stage committed)"
"$BIN" demo --seed 7 --queue-cap 8 --slow-consumer-ms 2 --manifest-version 1 --work-dir "$STATE"
code=$?
[ "$code" -eq 0 ] && pass "clean demo exit 0" || { echo "clean demo exit=$code"; fail=1; }

note "2) corrupt truncated with --expect-error (expect non-zero with offset/phase/reason)"
"$BIN" demo --seed 7 --corrupt truncated --expect-error --work-dir "$STATE-corrupt"
code=$?
[ "$code" -eq 2 ] && pass "corrupt demo exit 2 with structured error" || { echo "corrupt exit=$code"; fail=1; }

note "3) interrupted migration keeps the original manifest (expect exit 2)"
before="$(shasum -a 256 "$STATE/asset-manifest.json" | awk '{print $1}')"
"$BIN" demo --seed 7 --interrupt-migration after-stage --work-dir "$STATE"
code=$?
after="$(shasum -a 256 "$STATE/asset-manifest.json" | awk '{print $1}')"
if [ "$code" -eq 2 ] && [ "$before" = "$after" ]; then
  pass "interrupt exit 2 and original manifest byte-identical"
else
  echo "interrupt code=$code before=$before after=$after"; fail=1
fi

note "4) verify repeat stability (expect exit 0, stable digests and asset sets)"
"$BIN" verify --repeat 2 --print-json --work-dir "$STATE"
code=$?
[ "$code" -eq 0 ] && pass "verify exit 0, repeat run stable" || { echo "verify exit=$code"; fail=1; }

note "summary"
if [ "$fail" -eq 0 ]; then
  echo "ALL ACCEPTANCE CHECKS PASSED"
  exit 0
fi
echo "ACCEPTANCE FAILURES DETECTED"
exit 1
