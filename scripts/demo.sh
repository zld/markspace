#!/usr/bin/env bash
# End-to-end local demo: configure, build, render samples, run the pixel
# regression verify and the C++ self-test. No network, no GUI.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD="${ROOT}/build"
OUT="${1:-/tmp/transition-demo}"
WIDTH="${2:-160}"
HEIGHT="${3:-90}"
FPS="${4:-30}"

echo "== configure =="
cmake -S "${ROOT}" -B "${BUILD}"

echo "== build =="
cmake --build "${BUILD}"

echo "== self-test =="
"${BUILD}/transition_selftest"

echo "== render demo (${WIDTH}x${HEIGHT} @ ${FPS}fps) -> ${OUT} =="
rm -rf "${OUT}"
"${BUILD}/transition_render" demo \
  --width "${WIDTH}" --height "${HEIGHT}" --fps "${FPS}" --out "${OUT}"

echo "== verify pixels and hashes =="
"${BUILD}/transition_render" verify --out "${OUT}"

echo "== outputs =="
ls -1 "${OUT}" | sed "s|^|${OUT}/|" | head -8
echo "... ($(ls -1 "${OUT}" | wc -l | tr -d ' ') files total, incl. manifest)"
echo "demo.sh OK"
