#!/usr/bin/env bash
# Acceptance run for the mediacore processing kernel.
#
# Usage:
#   scripts/acceptance.sh [output-dir]
#
# Runs the documented demo twice (second run with --resume), verifies the
# exported artifacts, and asserts the acceptance properties. Exit status is
# the script's own verdict: 0 means every check held.
set -u

OUT=${1:-./tmp}
BIN=${BIN:-./build/mediacore}
mkdir -p "$(dirname "$BIN")" "$OUT"

echo "== build =="
go build -o "$BIN" ./cmd/mediacore || exit 1

DEMO_ARGS=(
  demo
  --seed 41
  --tracks 2
  --fragments 24
  --inject truncation,tfdt-regression,index-gap
  --checkpoint "$OUT/checkpoint.json"
  --resume
  --frames "$OUT/frames.jsonl"
  --manifest "$OUT/manifest.json"
)

echo "== demo (first run) =="
"$BIN" "${DEMO_ARGS[@]}" >"$OUT/demo1.txt" 2>&1
first=$?
cat "$OUT/demo1.txt"
echo "== demo (resume) =="
"$BIN" "${DEMO_ARGS[@]}" >"$OUT/demo2.txt" 2>&1
second=$?
cat "$OUT/demo2.txt"

echo "== verify =="
"$BIN" verify --manifest "$OUT/manifest.json" --frames "$OUT/frames.jsonl" >"$OUT/verify.txt" 2>&1
verify=$?
cat "$OUT/verify.txt"

value() { grep -m1 "^$1=" "$2" | cut -d= -f2-; }
fail=0
check() { # name expected actual
  if [ "$2" = "$3" ]; then
    echo "ok   $1=$3"
  else
    echo "FAIL $1: expected $2, got $3"; fail=1
  fi
}

check fragments 24   "$(value fragments "$OUT/demo1.txt")"
check tracks 2       "$(value tracks "$OUT/demo1.txt")"
check faults 3       "$(value faults "$OUT/demo1.txt")"
check offset_error_bytes 0 "$(value offset_error_bytes "$OUT/demo1.txt")"
check recovered true "$(value recovered "$OUT/demo1.txt")"
check resumed true   "$(value resumed "$OUT/demo2.txt")"
check invariant_failures 0 "$(value invariant_failures "$OUT/verify.txt")"

sha1=$(value sha256 "$OUT/demo1.txt")
sha2=$(value sha256 "$OUT/demo2.txt")
check stable_sha256 "$sha1" "$sha2"

resume=$(value resume_ms "$OUT/demo2.txt")
awk -v r="$resume" 'BEGIN { exit !(r < 2000) }' || { echo "FAIL resume_ms=$resume >= 2000"; fail=1; }

case $first  in 2) : ;; *) echo "FAIL demo exit=$first, want 2"; fail=1 ;; esac
case $second in 2) : ;; *) echo "FAIL resume exit=$second, want 2"; fail=1 ;; esac
case $verify in 0) : ;; *) echo "FAIL verify exit=$verify, want 0"; fail=1 ;; esac

if [ "$fail" -ne 0 ]; then
  echo "acceptance: FAILED"
  exit 1
fi
echo "acceptance: PASSED"
