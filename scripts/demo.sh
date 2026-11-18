#!/usr/bin/env bash
# Builds (if needed), generates deterministic fixtures and runs the acceptance
# demo. Everything is local: no network, no external services.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BUILD_DIR="${BUILD_DIR:-build}"
BIN="$BUILD_DIR/lutcheck"
SIZE="${SIZE:-17}"
BINS="${BINS:-16}"
SPACE="${SPACE:-rec709}"
THREADS="${THREADS:-4}"

if [[ ! -x "$BIN" ]]; then
  cmake -S . -B "$BUILD_DIR" -G Ninja -DCMAKE_BUILD_TYPE=Release
  cmake --build "$BUILD_DIR"
fi

echo "== fixture generation =="
mkdir -p fixtures
"$BIN" demo --generate-fixtures --size "$SIZE" --space "$SPACE" \
  --compare scalar,parallel --histogram-bins "$BINS" --threads "$THREADS" \
  --json > fixtures/report.json
cat fixtures/report.json
echo

echo "== acceptance checks =="
python3 -I - fixtures/report.json "$SIZE" "$BINS" <<'PY'
import json
import sys

path, size, bins = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
with open(path, encoding="utf-8") as handle:
    report = json.load(handle)

def require(condition, message):
    if not condition:
        raise SystemExit(f"FAIL: {message}")

require(report["grid_size"] == size, f"grid_size is {report['grid_size']}")
deviation = report["max_deviation_rgb"]
require(set(deviation) == {"r", "g", "b"}, "deviation channels missing")
require(all(isinstance(v, (int, float)) and v == v and abs(v) <= 1e-6
            for v in deviation.values()),
        f"max_deviation_rgb too large: {deviation}")
for key in ("histogram_before", "histogram_after"):
    hist = report[key]
    require(hist["bins"] == bins, f"{key} bins is {hist['bins']}")
    for channel in ("r", "g", "b"):
        require(len(hist[channel]) == bins, f"{key}.{channel} length")
        require(sum(hist[channel]) > 0, f"{key}.{channel} empty")
pixels = report["pixels"]
require(10_000 <= pixels < 100_000, f"pixels out of expected scale: {pixels}")
print("acceptance: OK")
PY

echo "== negative cases =="
set +e
"$BIN" demo --size 1 >/dev/null 2>&1
usage_code=$?
printf 'P6\n2 2\n255\n\x01\x02' > fixtures/truncated.ppm
"$BIN" render --input fixtures/truncated.ppm --lut fixtures/grade.cube \
  --compare scalar >/dev/null 2>&1
input_code=$?
"$BIN" demo --lut fixtures/grade.cube --size 9 --compare scalar \
  --input fixtures/sample.ppm --threshold 0 >/dev/null 2>&1
threshold_code=$?
set -e
echo "usage error exit:    $usage_code (expect 2)"
echo "input error exit:    $input_code (expect 3)"
echo "threshold exit:      $threshold_code (expect 0; paths are identical)"
[[ "$usage_code" -eq 2 ]] || { echo "FAIL: usage exit code"; exit 1; }
[[ "$input_code" -eq 3 ]] || { echo "FAIL: input exit code"; exit 1; }

echo "demo: OK"
