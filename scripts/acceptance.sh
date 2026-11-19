#!/usr/bin/env bash
#
# acceptance.sh — end-to-end validation of rtspcheck on macOS.
#
# Exercises both transport modes against the in-process loopback mock
# source and asserts the report contract: required JSON fields,
# positive counts, loss-induced incomplete frames, identical state
# transition counts across modes, a clean no-fault baseline and
# deterministic re-runs. Exits non-zero on the first failed assertion.
set -euo pipefail

cd "$(dirname "$0")/.."

# Pin to the local toolchain so `go mod` never upgrades the go directive.
export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"

WORKDIR="$(mktemp -d)"
BIN="$WORKDIR/rtspcheck"
trap 'rm -rf "$WORKDIR"' EXIT

pass=0
fail=0
note()  { printf '\n--- %s ---\n' "$1"; }
check() {
  local desc="$1"; shift
  if "$@"; then
    printf 'ok   %s\n' "$desc"; pass=$((pass + 1))
  else
    printf 'FAIL %s\n' "$desc"; fail=$((fail + 1))
  fi
}

# Extract a numeric JSON field.
jfield() {
  local key="$1" file="$2"
  sed -n "s/.*\"$key\": *\\([-0-9]*\\).*/\\1/p" "$file" | head -1
}

field_present_positive() {
  local key="$1" file="$2" v
  v="$(jfield "$key" "$file")"
  [ -n "$v" ] && [ "$v" -gt 0 ]
}

note "build and unit tests"
go mod download
go test ./...
go build -o "$BIN" ./cmd/rtspcheck

TCP_OUT="$WORKDIR/tcp.json"
UDP_OUT="$WORKDIR/udp.json"
TCP_OUT2="$WORKDIR/tcp2.json"
CLEAN_OUT="$WORKDIR/clean.json"

note "fault-injected run: tcp and udp"
"$BIN" --mode tcp --frames 120 --seed 41 --drop-every 19 \
  --reorder-window 8 --format json 2>/dev/null > "$TCP_OUT"
"$BIN" --mode udp --frames 120 --seed 41 --drop-every 19 \
  --reorder-window 8 --format json 2>/dev/null > "$UDP_OUT"

for f in "$TCP_OUT" "$UDP_OUT"; do
  for key in state_transitions sequence_gaps complete_frames; do
    check "field $key present and positive" field_present_positive "$key" "$f"
  done
done

TCP_COMPLETE="$(jfield complete_frames "$TCP_OUT")"
UDP_COMPLETE="$(jfield complete_frames "$UDP_OUT")"
TCP_TRANS="$(jfield state_transitions "$TCP_OUT")"
UDP_TRANS="$(jfield state_transitions "$UDP_OUT")"

note "frame and transition contract"
check "tcp complete frames below 120 due to drops" test "$TCP_COMPLETE" -lt 120
check "udp complete frames below 120 due to drops" test "$UDP_COMPLETE" -lt 120
check "transition counts identical across modes" test "$TCP_TRANS" -eq "$UDP_TRANS"
check "both modes report six transitions" test "$TCP_TRANS" -eq 6

note "no-fault baseline"
"$BIN" --mode tcp --frames 120 --seed 41 --format json 2>/dev/null > "$CLEAN_OUT"
check "baseline reassembles 120 complete frames" \
  test "$(jfield complete_frames "$CLEAN_OUT")" -eq 120
check "baseline has zero sequence gaps" \
  test "$(jfield sequence_gaps "$CLEAN_OUT")" -eq 0
check "baseline has zero malformed packets" \
  test "$(jfield malformed_packets "$CLEAN_OUT")" -eq 0

note "determinism"
"$BIN" --mode tcp --frames 120 --seed 41 --drop-every 19 \
  --reorder-window 8 --format json 2>/dev/null > "$TCP_OUT2"
check "identical parameters yield identical reports" diff -q "$TCP_OUT" "$TCP_OUT2"

note "exit-code contract"
set +e
"$BIN" --mode bogus >/dev/null 2>&1
bad_mode_code=$?
"$BIN" --mode tcp --fail-stage describe >/dev/null 2>&1
fail_stage_code=$?
set -e
check "invalid mode exits 2" test "$bad_mode_code" -eq 2
check "negotiation failure exits 1" test "$fail_stage_code" -eq 1

note "summary format is a single line"
line="$("$BIN" --mode tcp --frames 10 --seed 1 --format summary 2>/dev/null)"
summary_ok() {
  case "$1" in
    *"mode=tcp"*"state_transitions=6"*"complete_frames=10"*) return 0 ;;
    *) return 1 ;;
  esac
}
check "summary contains key=value fields" summary_ok "$line"

printf '\n==============================\n'
printf 'acceptance: %d passed, %d failed\n' "$pass" "$fail"
test "$fail" -eq 0
