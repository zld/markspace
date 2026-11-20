#!/usr/bin/env bash
#
# demo.sh - reproducible end-to-end demonstration of the hlscheck pipeline.
#
# It generates two synthetic H.264 transport streams, slices them into
# keyframe-aligned HLS segments, validates the clean output, injects
# deterministic faults and re-validates to show the failure codes.
#
# Everything runs locally under a fresh temporary directory. No network or
# GUI is used.
set -euo pipefail

# Resolve the repository root from this script's location.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

GO="mise exec go@1.22.12 -- go"
BIN="/tmp/hlscheck"
WORK="$(mktemp -d)"

echo "==> Building hlscheck"
${GO} build -o "${BIN}" ./cmd/hlscheck

echo
echo "==> Clean pipeline: generate, slice and validate 11s @25fps"
echo "    work directory: ${WORK}"
"${BIN}" demo \
  --work "${WORK}" \
  --duration 11s \
  --rates 350000,800000 \
  --target 2s

echo
echo "==> Injecting faults: drop 800000:2 and swap 350000 positions 1 and 3"
"${BIN}" fault \
  --root "${WORK}/hls" \
  --index "${WORK}/hls/index.json" \
  --drop 800000:2 \
  --swap 350000:1:3

echo
echo "==> Re-validating (exit code 2 means media consistency failure)"
set +e
"${BIN}" validate \
  --root "${WORK}/hls" \
  --index "${WORK}/hls/index.json"
VALIDATE_EXIT=$?
set -e

echo
echo "==> Generated tree:"
find "${WORK}" -type f | sort

echo
echo "validate exit code: ${VALIDATE_EXIT} (expected 2)"
echo "work directory retained at: ${WORK}"
exit "${VALIDATE_EXIT}"
