#!/bin/bash
# Builds lutcheck from scratch, runs CTest and the acceptance demo, then checks
# the summary against the documented acceptance criteria.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD="${BUILD_DIR:-$ROOT/build}"
DEMO_ARGS=(demo --size 17 --pixels 65536 --input-space srgb --lut-space linear-rgb
           --threads 4 --boundary clamp --format json)
fail() { echo "verify: $*" >&2; exit 1; }

command -v cmake >/dev/null 2>&1 || fail "cmake not found (brew install cmake)"
echo "== configure =="
cmake -S "$ROOT" -B "$BUILD" -DCMAKE_BUILD_TYPE=Release >/dev/null
echo "== build =="
cmake --build "$BUILD" --parallel
echo "== ctest =="
ctest --test-dir "$BUILD" --output-on-failure
echo "== acceptance demo =="
output="$("$BUILD/lutcheck" "${DEMO_ARGS[@]}")" || fail "demo exited non-zero"
printf '%s\n' "$output"
printf '%s\n' "$output" | grep -q 'lut_size:17' || fail "missing lut_size:17"
printf '%s\n' "$output" | grep -q 'pixels:65536' || fail "missing pixels:65536"
printf '%s\n' "$output" | grep -q 'max_abs_diff:\[0.000000,0.000000,0.000000\]' \
    || fail "max_abs_diff must be <= 1e-6"
printf '%s\n' "$output" | grep -q 'histogram_before' || fail "missing histogram_before"
printf '%s\n' "$output" | grep -q 'histogram_after' || fail "missing histogram_after"
python3 - "$output" <<'PY' || fail "histogram validation failed"
import json, sys
report = json.loads(sys.argv[1])
for key in ("histogram_before", "histogram_after"):
    for channel in "rgb":
        buckets = report[key][channel]
        assert len(buckets) == 16, (key, channel, len(buckets))
        assert sum(buckets) == report["pixels"], (key, channel, sum(buckets))
PY
echo "== repeatability =="
second="$("$BUILD/lutcheck" "${DEMO_ARGS[@]}")"
[ "$output" = "$second" ] || fail "repeated run produced a different summary"
echo "verify: ok"
