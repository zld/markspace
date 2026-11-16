#!/usr/bin/env bash
#
# Reproducible local demo for the avsync kernel.
#
# Generates a synthetic 8-second A/V sample with FFmpeg, builds the project,
# runs the kernel state tests, then decodes the sample and prints A/V sync
# statistics. Everything is local: no network access and no GUI/device use.
#
# Usage: scripts/demo.sh
#
set -euo pipefail

# Resolve the repository root regardless of the caller's working directory.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

BUILD_DIR="${ROOT_DIR}/build"
SAMPLE="${ROOT_DIR}/sample.mp4"
REPORT="${ROOT_DIR}/report.json"

echo "==> Checking toolchain (ffmpeg, cmake, pkg-config)"
for tool in ffmpeg cmake pkg-config; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "error: '${tool}' not found. Install with: brew install ffmpeg cmake pkg-config" >&2
    exit 2
  fi
done

echo "==> Configuring and building (Release)"
cmake -S . -B "${BUILD_DIR}" -DCMAKE_BUILD_TYPE=Release
cmake --build "${BUILD_DIR}" -j

echo "==> Running kernel tests"
ctest --test-dir "${BUILD_DIR}" --output-on-failure

echo "==> Generating 8-second synthetic sample (320x180@30 video, 48 kHz AAC)"
ffmpeg -hide_banner -loglevel error -y \
  -f lavfi -i 'testsrc2=size=320x180:rate=30:duration=8' \
  -f lavfi -i 'sine=frequency=440:sample_rate=48000:duration=8' \
  -c:v mpeg4 -c:a aac -shortest "${SAMPLE}"

echo "==> Running A/V sync kernel (jitter=12ms, seed=7, realtime factor=2)"
"${BUILD_DIR}/avsync" run \
  --input "${SAMPLE}" \
  --jitter-ms 12 \
  --seed 7 \
  --realtime-factor 2 \
  --json "${REPORT}"

echo
echo "==> JSON report written to ${REPORT}"
echo "==> Done. Expected: ~240 video frames, 384000 audio samples, exit code 0."
