#!/usr/bin/env bash
# End-to-end demonstration for the MP4 fragmented repackager.
# Purely local; regenerates deterministic fixtures, runs the repacker on the
# healthy set and every fault set, then executes the CTest suite.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD="$ROOT/build"
SAMPLE="$BUILD/sample"

echo "== 1. configure & build =="
cmake -S "$ROOT" -B "$BUILD" -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_PREFIX_PATH="$(brew --prefix 2>/dev/null || echo /opt/homebrew)"
cmake --build "$BUILD" --parallel

echo
echo "== 2. generate deterministic fixtures =="
rm -rf "$SAMPLE"
"$BUILD/make_fixtures" --out "$SAMPLE" --all

echo
echo "== 3. repack the healthy six-fragment set (JSON summary) =="
"$BUILD/mp4_repack" --input "$SAMPLE/input" \
    --output "$SAMPLE/output" --json
HEALTHY=$?
echo "healthy-set exit code: $HEALTHY (expected 0)"

echo
echo "== 4. fault-injection matrix (exit codes: 0 ok, 2 usage, 3 structural, 4 rewrite) =="
for fault in missing outoforder duplicate length drift encrypted truncated; do
    OUT="$SAMPLE/faults/$fault/output"
    rm -rf "$OUT"
    "$BUILD/mp4_repack" --input "$SAMPLE/faults/$fault/input" \
        --output "$OUT" >/tmp/mp4repack_$fault.log 2>&1
    code=$?
    printf '  %-12s -> exit %d\n' "$fault" "$code"
done
echo "  (missing/outoforder are recoverable: 0; duplicate/length/drift/"
echo "   encrypted/truncated block writing: 3)"

echo
echo "== 5. idempotent re-run overwrites without leaving temp files =="
"$BUILD/mp4_repack" --input "$SAMPLE/input" --output "$SAMPLE/output" >/dev/null
echo "  re-run exit code: $?"
if find "$SAMPLE/output" -name '.mp4repack.*' | grep -q .; then
    echo "  ERROR: stale temp files found" >&2
    exit 4
else
    echo "  no stale temp/backup files"
fi

echo
echo "== 6. ctest =="
ctest --test-dir "$BUILD" --output-on-failure

if [ "$HEALTHY" -ne 0 ]; then
    echo "demo FAILED: healthy set did not repack cleanly" >&2
    exit 4
fi
echo
echo "demo OK"
