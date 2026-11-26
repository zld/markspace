#!/usr/bin/env bash
#
# acceptance.sh - run every mediamerge acceptance scenario and assert the
# documented terminal metrics and exit codes. Purely local, no services.
set -euo pipefail

cd "$(dirname "$0")/.."

# Prefer the Homebrew Go 1.23 toolchain when present; otherwise use PATH.
if [ -x "$(command -v /opt/homebrew/opt/go@1.23/bin/go 2>/dev/null || true)" ]; then
  export PATH="/opt/homebrew/opt/go@1.23/bin:$PATH"
fi

OUT_ROOT="${OUT_ROOT:-/tmp/pwf-acceptance}"
rm -rf "$OUT_ROOT"
mkdir -p "$OUT_ROOT"

echo "== tidy / build / test =="
go mod tidy
go build ./...
go test ./...

BIN="$(pwd)/.tmpbin/mediamerge"
mkdir -p "$(dirname "$BIN")"
go build -o "$BIN" ./cmd/mediamerge
run() { "$BIN" "$@"; }

echo "== normal =="
run verify --case normal --out "$OUT_ROOT/normal" | tee "$OUT_ROOT/normal.txt"
grep -q "roundtrip=8/8"        "$OUT_ROOT/normal.txt"
grep -q "keyframe_hit=8/8"     "$OUT_ROOT/normal.txt"
grep -q "segments=4"           "$OUT_ROOT/normal.txt"
grep -q "hash_mismatch=0"      "$OUT_ROOT/normal.txt"

echo "== damaged =="
run verify --case damaged --policy mark-gaps --out "$OUT_ROOT/damaged" \
  | tee "$OUT_ROOT/damaged.txt"
grep -q "roundtrip=7/7"        "$OUT_ROOT/damaged.txt"
grep -q "rejected=1"           "$OUT_ROOT/damaged.txt"
grep -q "gap=1"                "$OUT_ROOT/damaged.txt"
grep -q "keyframe_hit=8/8"     "$OUT_ROOT/damaged.txt"

echo "== conflict (expect exit code 2) =="
set +e
run verify --case conflict --policy strict --out "$OUT_ROOT/conflict" \
  | tee "$OUT_ROOT/conflict.txt"
code=${PIPESTATUS[0]}
set -e
[ "$code" -eq 2 ] || { echo "conflict exit=$code, want 2"; exit 1; }
grep -q "conflicts=1"          "$OUT_ROOT/conflict.txt"
grep -q "rollback=1"           "$OUT_ROOT/conflict.txt"
grep -q "manifest_unchanged=1" "$OUT_ROOT/conflict.txt"

echo "== recovery =="
run verify --case recovery --interrupt-after 2 --out "$OUT_ROOT/recovery" \
  | tee "$OUT_ROOT/recovery-interrupt.txt"
run resume --out "$OUT_ROOT/recovery" | tee "$OUT_ROOT/recovery-resume.txt"
grep -q "replayed=0"          "$OUT_ROOT/recovery-resume.txt"
grep -q "duplicate_cues=0"    "$OUT_ROOT/recovery-resume.txt"
grep -q "hash_match=4/4"      "$OUT_ROOT/recovery-resume.txt"

echo
echo "ALL ACCEPTANCE CHECKS PASSED"
