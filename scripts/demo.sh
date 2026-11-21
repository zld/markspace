#!/usr/bin/env bash
# End-to-end demonstration for mp4flow, fully local, no external media.
# Runs the exact acceptance commands and prints every exit code.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/build/mp4flow"
WORK="${1:-/tmp/fmp4-demo}"
GAP="${2:-/tmp/fmp4-gap}"

if ! test -x "$BIN"; then
  echo "binary missing; build first:" >&2
  echo "  cmake -S . -B build -DCMAKE_PREFIX_PATH=\"\$(brew --prefix catch2)\" -DCMAKE_BUILD_TYPE=Release" >&2
  echo "  cmake --build build --parallel" >&2
  exit 5
fi

echo "== 1. generate valid fixture =="
"$BIN" fixtures --work "$WORK" --case valid

echo
echo "== 2. repack (expect rc 0) =="
"$BIN" repack \
  --init-dir "$WORK/init" \
  --media-dir "$WORK/media" \
  --out "$WORK/out" \
  --expect-seq 101:105 \
  --max-correction-ms 3000
echo "repack rc=$?"

echo
echo "== 3. re-verify the published output (expect rc 0) =="
"$BIN" verify --init-dir "$WORK/init" --media-dir "$WORK/out" --expect-seq 1:5
echo "verify-output rc=$?"

echo
echo "== 4. gap fixture and verify (expect rc 4) =="
"$BIN" fixtures --work "$GAP" --case gap
"$BIN" verify --init-dir "$GAP/init" --media-dir "$GAP/media" --expect-seq 101:105
echo "verify-gap rc=$? (4 = missing expected sequence)"
