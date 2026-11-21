#!/usr/bin/env bash
# End-to-end demonstration of lutctl. Runs entirely from a clean build
# directory under /tmp and never touches network or external services.
set -euo pipefail

BUILD_DIR="${BUILD_DIR:-/tmp/lut-build}"
DEMO_DIR="${DEMO_DIR:-/tmp/lut-demo}"

echo "== configure =="
cmake -S . -B "${BUILD_DIR}" -DCMAKE_BUILD_TYPE=Release

echo "== build =="
cmake --build "${BUILD_DIR}" -j

echo "== tests =="
ctest --test-dir "${BUILD_DIR}" --output-on-failure

echo "== fixtures =="
"${BUILD_DIR}/lutctl" fixture-gen --out "${DEMO_DIR}" --patch-space display-p3

echo "== bake (srgb source -> display-p3, 33 grid) =="
"${BUILD_DIR}/lutctl" bake \
    --cube "${DEMO_DIR}/source.cube" \
    --source-space srgb \
    --target-space display-p3 \
    --size 33 \
    --probe-grid 25 \
    --patches "${DEMO_DIR}/patches.ppm" \
    --out "${DEMO_DIR}/baked.cube" \
    --max-error 0.02

echo "== probe =="
"${BUILD_DIR}/lutctl" probe \
    --cube "${DEMO_DIR}/baked.cube" \
    --space display-p3 \
    --rgb 0.20,0.40,0.60

echo "== render =="
"${BUILD_DIR}/lutctl" render \
    --cube "${DEMO_DIR}/baked.cube" \
    --space display-p3 \
    --input "${DEMO_DIR}/patches.ppm" \
    --output "${DEMO_DIR}/graded.ppm" \
    --tile-rows 16

echo "== outputs =="
ls -l "${DEMO_DIR}"
echo "demo complete"
