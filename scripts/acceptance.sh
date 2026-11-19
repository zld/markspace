#!/usr/bin/env bash
# Full local acceptance sequence for h264scan. Purely local, no network.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD="$ROOT/build"
FIX=/tmp/gop_fixture.h264

echo "== configure =="
cmake -S "$ROOT" -B "$BUILD" -DCMAKE_BUILD_TYPE=Release

echo "== build =="
cmake --build "$BUILD"

echo "== generate fixture =="
"$BUILD/tools/fixture_gen" --output "$FIX"

echo "== strict JSON scan (expect exit 0) =="
"$BUILD/tools/h264scan" --input "$FIX" --json --strict

echo
echo "== ctest =="
ctest --test-dir "$BUILD" --output-on-failure
