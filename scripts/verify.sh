#!/bin/bash
# Build lutcheck, run CTest, then check the acceptance demo against the
# documented contract (exit codes, summary literals, histogram totals,
# repeatability and the argument/LUT error paths).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD="${BUILD_DIR:-$ROOT/build}"
BIN="$BUILD/lutcheck"
DEMO_ARGS=(demo --size 17 --pixels 65536 --input-space srgb --lut-space linear-rgb
           --threads 4 --boundary clamp --format json)
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "verify: $*" >&2; exit 1; }

command -v cmake >/dev/null 2>&1 || fail "cmake not found (brew install cmake)"

echo "== configure =="
cmake -S "$ROOT" -B "$BUILD" -DCMAKE_BUILD_TYPE=Release >/dev/null

echo "== build =="
cmake --build "$BUILD" --parallel

echo "== ctest =="
ctest --test-dir "$BUILD" --output-on-failure

echo "== acceptance demo =="
output="$("$BIN" "${DEMO_ARGS[@]}")" || fail "demo exited non-zero"
printf '%s\n' "$output"
for literal in 'lut_size:17' 'pixels:65536' \
               'max_abs_diff:[0.000000,0.000000,0.000000]' \
               'histogram_before' 'histogram_after'; do
    printf '%s\n' "$output" | grep -qF -- "$literal" || fail "missing $literal"
done
python3 -I - "$output" <<'PY' || fail "report contract violated"
import json, sys
report = json.loads(sys.argv[1])
assert report["lut_size"] == 17, report["lut_size"]
assert report["pixels"] == 65536, report["pixels"]
assert max(report["max_abs_diff"]) <= 1e-6, report["max_abs_diff"]
for key in ("histogram_before", "histogram_after"):
    for channel in "rgb":
        buckets = report[key][channel]
        assert len(buckets) == 16, (key, channel, len(buckets))
        assert sum(buckets) == report["pixels"], (key, channel, sum(buckets))
PY

echo "== repeatability =="
second="$("$BIN" "${DEMO_ARGS[@]}")"
[ "$output" = "$second" ] || fail "repeated run produced a different summary"

echo "== exit codes =="
expect_failure() {
    local code=$1; shift
    set +e
    "$@" >/dev/null 2>"$TMP/err"
    local actual=$?
    set -e
    [ "$actual" -eq "$code" ] || { cat "$TMP/err" >&2; fail "expected exit $code, got $actual: $*"; }
    [ -s "$TMP/err" ] || fail "no diagnostic on stderr: $*"
}
expect_failure 2 "$BIN" demo --threads 0
expect_failure 2 "$BIN" demo --size 66
expect_failure 2 "$BIN" demo --pixels 16777217
expect_failure 2 "$BIN" demo --boundary wrap
expect_failure 3 "$BIN" demo --lut-space sepia
printf 'LUT_3D_SIZE 2\n0 0 0\n' >"$TMP/truncated.cube"
expect_failure 3 "$BIN" demo --lut-file "$TMP/truncated.cube"
printf 'LUT_3D_SIZE 2\nDOMAIN_MIN 1 1 1\nDOMAIN_MAX 1 1 1\n' >"$TMP/zero.cube"
expect_failure 3 "$BIN" demo --lut-file "$TMP/zero.cube"
expect_failure 4 "$BIN" demo --pixels 64 --output "$TMP/missing/dir/report.json"
# XYZ pixel data converts outside [0,1]; reject must stop the run.
expect_failure 4 "$BIN" demo --size 17 --pixels 512 --input-space linear-rgb \
    --lut-space xyz --boundary reject

echo "== external LUT =="
"$BIN" demo --size 9 --pixels 1024 --lut-order bgr --format json \
    | grep -qF 'max_abs_diff:[0.000000,0.000000,0.000000]' \
    || fail "BGR-ordered generated LUT changed the result"

echo "verify: ok"
