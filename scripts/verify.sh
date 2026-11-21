#!/usr/bin/env bash
#
# verify.sh — One-command acceptance flow for the ASS subtitle pipeline.
# Builds from scratch, runs the demo render and executes the CTest suite.
# Designed for local macOS (Apple Silicon and Intel); no network access.
#
set -euo pipefail

# Resolve the repository root regardless of the caller's working directory.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

BUILD_DIR="${BUILD_DIR:-build}"
OUT_DIR="${OUT_DIR:-out}"
CANVAS="${CANVAS:-1280x720}"

echo "==> Toolchain"
cmake --version | head -1
pkg-config --modversion libass | sed 's/^/libass /'

echo "==> Configure"
cmake -S . -B "${BUILD_DIR}" -DCMAKE_BUILD_TYPE=Release

echo "==> Build"
cmake --build "${BUILD_DIR}" --parallel

echo "==> Demo render (canvas ${CANVAS})"
rm -rf "${OUT_DIR}"
"${BUILD_DIR}/ass_pipeline" demo \
    --canvas "${CANVAS}" \
    --output "${OUT_DIR}" \
    --json "${OUT_DIR}/summary.json"

echo "==> Verify manifest and PNG outputs"
python3 - "${OUT_DIR}" <<'PY'
import json
import pathlib
import sys

out = pathlib.Path(sys.argv[1])
data = json.loads((out / "summary.json").read_text())
expected = {
    "styles": 6,
    "events": 12,
    "inheritance_errors": 2,
    "inline_tags": 48,
    "unknown_tags": 1,
    "rendered": 12,
}
for key, want in expected.items():
    got = data[key]
    assert got == want, f"{key}: expected {want}, got {got}"
pngs = sorted(out.glob("event_*.png"))
assert len(pngs) == data["rendered"], "PNG count mismatch"
for rec in data["records"]:
    assert isinstance(rec["width"], int) and rec["width"] > 0
    assert isinstance(rec["height"], int) and rec["height"] > 0
    assert len(rec["sha256"]) == 64
    int(rec["sha256"], 16)  # valid hexadecimal
    assert pathlib.Path(rec["png_path"]).exists()
print("manifest assertions passed: 12 PNGs, 64-hex SHA-256, positive sizes")
PY

echo "==> Integration tests"
ctest --test-dir "${BUILD_DIR}" --output-on-failure

echo "==> All verification steps completed successfully"
