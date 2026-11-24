#!/usr/bin/env bash
# acceptance.sh runs the documented verification for mediasync.
#
# It mirrors the commands in the task specification and additionally builds a
# local binary so the kernel's real exit codes are observable. Note that
# `go run` itself maps every non-zero child status to shell exit 1 and prints
# "exit status N"; the authoritative corruption code 2 is therefore checked on
# the compiled binary (and is also present as the JSON field "exit_code": 2).
#
# Exit: 0 when every rule passes, 1 otherwise.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BIN="${TMPDIR:-/tmp}/mediasync-acceptance-$$"
trap 'rm -f "$BIN"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "==> go mod tidy"
go mod tidy || fail "go mod tidy"

echo "==> go build ./..."
go build ./... || fail "go build"

echo "==> go test ./..."
go test ./... || fail "go test"

echo "==> build acceptance binary"
go build -o "$BIN" ./cmd/mediasync || fail "build binary"

WORK="$(mktemp -d)"
trap 'rm -f "$BIN"; rm -rf "$WORK"' EXIT

echo "==> demo (seed 17), twice for determinism"
"$BIN" demo --seed 17 --wrap-bits 16 --drift-ppm 120 \
  --late-window-ms 500 --workdir "$WORK/d1" > "$WORK/demo1.json" 2>"$WORK/demo1.err" \
  || fail "demo exited non-zero"
"$BIN" demo --seed 17 --wrap-bits 16 --drift-ppm 120 \
  --late-window-ms 500 --workdir "$WORK/d2" > "$WORK/demo2.json" 2>/dev/null \
  || fail "demo rerun exited non-zero"

sha1="$(sed -n 's/.*"sha256": "\([0-9a-f]\{64\}\)".*/\1/p' "$WORK/demo1.json" | head -1)"
sha2="$(sed -n 's/.*"sha256": "\([0-9a-f]\{64\}\)".*/\1/p' "$WORK/demo2.json" | head -1)"
[ -n "$sha1" ] || fail "demo: no sha256 in summary"
[ "$sha1" = "$sha2" ] || fail "demo: non-deterministic sha256 $sha1 != $sha2"

skew="$(sed -n 's/.*"max_av_skew_ms": \([0-9.]*\).*/\1/p' "$WORK/demo1.json" | head -1)"
awk "BEGIN{exit !($skew <= 20)}" || fail "demo: max A/V skew ${skew}ms exceeds 20ms"
echo "    sha256=$sha1 skew=${skew}ms"

echo "==> verify corrupt-truncated (expect exit 2, deviation 0)"
"$BIN" verify --fixture corrupt-truncated --expect-offset 4096 \
  --workdir "$WORK/v" > "$WORK/verify.json" 2>/dev/null
code=$?
[ "$code" -eq 2 ] || fail "verify exit code = $code, want 2"
grep -q '"error_count": [1-9]' "$WORK/verify.json" || fail "verify: error_count not non-zero"
grep -q '"offset_deviation": 0' "$WORK/verify.json" || fail "verify: offset deviation not within 1 byte"
echo "    exit=2, deviation=0"

echo "==> resume generated-long from checkpoint 2000"
"$BIN" resume --fixture generated-long --checkpoint-every 1000 \
  --interrupt-after 2500 --workdir "$WORK/r" > "$WORK/resume.json" 2>/dev/null \
  || fail "resume exited non-zero"
grep -q '"resumed_from": 2000' "$WORK/resume.json" || fail "resume: did not continue from event 2000"
grep -q "\"sha256\": \"$sha1\"" "$WORK/resume.json" \
  || fail "resume: digest does not match uninterrupted demo/reference run"
grep -q '"resume_elapsed_ms"' "$WORK/resume.json" || fail "resume: missing resume_elapsed_ms"
echo "    resumed_from=2000, digest matches"

echo "ALL ACCEPTANCE CHECKS PASSED"
