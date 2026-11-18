#!/usr/bin/env bash
#
# demo.sh — end-to-end demonstration for dashforge.
#
# It regenerates all fixtures inside the repository, runs the acceptance
# commands, verifies the required summary fields and exit codes, and finally
# exercises every negative fixture. No network access or external files are
# required; run from anywhere on macOS with the Go toolchain installed.
#
# Note: `go run` reports a wrapped exit code 1 for any non-zero program exit,
# so the acceptance commands below use `go run` exactly as specified for the
# success path, while exit-code assertions (2/3/4) invoke a locally built
# binary whose exit status is preserved.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

GEN_DIR="testdata/generated"
MPD_OUT="/tmp/demo.mpd"
BIN_DIR="$(mktemp -d)"
BIN="$BIN_DIR/dashforge"
trap 'rm -rf "$BIN_DIR"' EXIT
fail() { echo "demo FAILED: $*" >&2; exit 1; }

echo "==> [1/6] go test ./..."
go test ./...

echo
echo "==> [2/6] regenerate fixtures under ${GEN_DIR}"
rm -rf "$GEN_DIR"
go run ./cmd/dashforge fixtures --out "$GEN_DIR" --with-files
[ -f "$GEN_DIR/valid.json" ] || fail "valid fixture was not generated"
go build -o "$BIN" ./cmd/dashforge

echo
echo "==> [3/6] generate MPD (timescale=90000, tolerance-ms=20)"
GEN_SUMMARY="$(go run ./cmd/dashforge generate \
  --input "$GEN_DIR/valid.json" --output "$MPD_OUT" \
  --timescale 90000 --tolerance-ms 20)"
echo "$GEN_SUMMARY"
[ -f "$MPD_OUT" ] || fail "MPD was not written to $MPD_OUT"

for field in "representations=3" "segments=36" "timescale=90000" \
            "duration_ms=" "max_alignment_error_ms=" "first=" "last="; do
  echo "$GEN_SUMMARY" | grep -q "$field" || fail "summary missing field: $field"
done
# Alignment error must stay within the configured 20ms tolerance.
ERR="$(echo "$GEN_SUMMARY" | sed -n 's/^max_alignment_error_ms=//p')"
awk -v e="$ERR" 'BEGIN { exit !(e <= 20) }' || fail "alignment error $ERR exceeds 20ms"

echo
echo "==> [4/6] validate the MPD with --json"
VAL_SUMMARY="$(go run ./cmd/dashforge validate \
  --input "$GEN_DIR/valid.json" --mpd "$MPD_OUT" \
  --timescale 90000 --tolerance-ms 20 --json)"
echo "$VAL_SUMMARY"
echo "$VAL_SUMMARY" | grep -q '"status": "ok"' || fail "validate did not report status ok"
echo "$VAL_SUMMARY" | grep -q '"representations": 3' || fail "JSON summary field order/value mismatch"

echo
echo "==> [5/6] exit code mapping (2=args, 3=invalid, 4=io failure)"
set +e
"$BIN" generate --tolerance-ms -1 --input "$GEN_DIR/valid.json" >/dev/null 2>&1
[ $? -eq 2 ] || fail "negative tolerance did not map to exit code 2"
"$BIN" generate --input /no/such/metadata.json >/dev/null 2>&1
[ $? -eq 4 ] || fail "missing input did not map to exit code 4"
set -e

echo
echo "==> [6/6] negative fixtures must fail with exit code 3"
for sample in negative-offset negative-missing-number negative-duration-drift; do
  echo "-- $sample"
  set +e
  OUT="$("$BIN" generate \
    --input "$GEN_DIR/$sample.json" \
    --output "/tmp/demo-$sample.mpd" \
    --timescale 90000 --tolerance-ms 20 2>&1)"
  code=$?
  set -e
  echo "$OUT"
  [ "$code" -eq 3 ] || fail "$sample exited with $code, want 3"
  [ ! -e "/tmp/demo-$sample.mpd" ] || fail "$sample wrote an MPD despite validation failure"
  echo "$OUT" | grep -qE "rendition|finding" || fail "$sample did not report rendition-level detail"
done

echo
echo "demo OK: MPD at $MPD_OUT, all acceptance checks passed"
