#!/usr/bin/env bash
# demo.sh - end-to-end walkthrough of offline packaging and license validation.
#
# Runs the acceptance scenario, then the corruption, drop, replay and expiry
# probes. Every step prints the exit code so a failing check is obvious.
#
# Usage: scripts/demo.sh [--keep]
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

bin="$(mktemp -d)/mediakey"
trap 'rm -rf "$(dirname "$bin")"' EXIT

echo "== building mediakey =="
go build -o "$bin" ./cmd/mediakey

run_step() {
  local label="$1"; shift
  echo
  echo "== $label =="
  set +e
  "$bin" "$@"
  local code=$?
  set -e
  echo "-- exit code: $code"
  return 0
}

# 12 segments, a rotation every 3 segments => 3 rotations (key versions 1-4),
# a 60s license, and a clock advanced 75s for the expiry boundary.
run_step "acceptance: 12 segments, rotate every 3, ttl 60s, clock +75s, replay check" \
  demo --segments 12 --rotate-every 3 --license-ttl 60s --advance-clock 75s --replay-check

# Expected, in order: summary ok_count=12 failed_count=0; four rotations of
# key id k1..k4; every segment status=ok reason=NONE; replay rejected; and an
# advanced-clock probe showing LICENSE_EXPIRED for the license and KEY_EXPIRED
# for the key versions.

run_step "corruption: segment 5 => AUTH_FAILED, exit 2" \
  demo --segments 12 --rotate-every 3 --corrupt-segment 5

run_step "missing: segment 8 => SEGMENT_MISSING, exit 2" \
  demo --segments 12 --rotate-every 3 --drop-segment 8

run_step "invalid flag => exit 1" \
  demo --segments 12 --corrupt-segment 99

echo
echo "== done =="
echo "A clean run exits 0; a run with validation failures exits 2."
