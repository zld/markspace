#!/usr/bin/env bash
# verify.sh runs the acceptance record end to end and checks each bound.
# Usage: ./scripts/verify.sh   (from the project directory)
set -euo pipefail

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
fail=0

check() { # name expected op actual
  local name="$1" want="$2" op="$3" got="$4"
  if awk -v w="$want" -v g="$got" -v op="$op" 'BEGIN{exit !((op=="<="&&g<=w)||(op==">="&&g>=w)||(op=="=="&&g==w))}'; then
    echo "PASS $name ($got $op $want)"
  else
    echo "FAIL $name ($got $op $want)"; fail=1
  fi
}

echo "== build and test"
go build ./...
go test ./... >/dev/null

echo "== acceptance run"
OUT="$(go run ./cmd/mediad demo --seed 7 --loss 0.04 --chunk-size 64 --report stdout --work-dir "$WORK/run")"
echo "$OUT"

val() { printf '%s\n' "$OUT" | awk -F= -v k="$1" '$1==k{print $2}'; }

check av_sync_ms 40 '<=' "$(val av_sync_ms)"
check loss_recovery_rate 0.95 '>=' "$(val loss_recovery_rate)"
check latency_p95_ms 100 '<=' "$(val latency_p95_ms)"
check aligned_segments 1 '>=' "$(val aligned_segments)"
check index_reused true '==' "$(val index_reused)"
check replacement_index_reused false '==' "$(val replacement_index_reused)"
check damaged_rejected true '==' "$(val damaged_rejected)"
# Boundary error must stay within one frame; the demo runs at 30 fps (33333 µs).
check max_boundary_error_us 33333 '<=' "$(val max_boundary_error_us)"

if [ -z "$(val manifest_sha256)" ]; then echo "FAIL manifest_sha256 missing"; fail=1; fi

echo "== resume equals continuous"
CONT="$(val manifest_sha256)"
STOP_WORK="$WORK/stop"
go run ./cmd/mediad demo --seed 7 --loss 0.04 --chunk-size 64 --stop-after-chunks 2 --work-dir "$STOP_WORK" >/dev/null
RES="$(go run ./cmd/mediad demo --seed 7 --loss 0.04 --chunk-size 64 --work-dir "$STOP_WORK")"
RESUME="$(printf '%s\n' "$RES" | awk -F= '$1=="manifest_sha256"{print $2}')"
check resume_digest "$CONT" '==' "$RESUME"

if [ "$fail" -ne 0 ]; then
  echo "== FAILED"
  exit 1
fi
echo "== all checks passed"
