#!/usr/bin/env bash
# End-to-end acceptance: build, generate the deterministic fixture, run the
# diagnostic CLI twice, and validate JSON fields plus run-to-run determinism.
# Designed for ctest but also runnable directly from the project root:
#   tests/run_acceptance.sh <build-dir>
set -euo pipefail

BUILD_DIR="${1:-build}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PYTHON="${PYTHON:-python3}"

BIN="$BUILD_DIR/pw_sync"
FIX="$BUILD_DIR/sync_fixture.mp4"
R1="$BUILD_DIR/report_run1.json"
R2="$BUILD_DIR/report_run2.json"
BROKEN_DIR="$BUILD_DIR/broken_input"
BROKEN="$BROKEN_DIR/broken.mp4"

echo "== configure and build =="
cmake -S "$ROOT_DIR" -B "$BUILD_DIR" -DCMAKE_BUILD_TYPE=Release
cmake --build "$BUILD_DIR" --parallel

echo "== generate fixture =="
"$BIN" --generate-fixture "$FIX"
test -s "$FIX"

echo "== run analysis twice (determinism pair) =="
"$BIN" --input "$FIX" --clock audio --late-ms 35 \
       --path-a-delay-ms 4 --path-b-delay-ms 17 --json --output "$R1"
"$BIN" --input "$FIX" --clock audio --late-ms 35 \
       --path-a-delay-ms 4 --path-b-delay-ms 17 --json --output "$R2"

echo "== validate JSON contract and determinism =="
"$PYTHON" -I "$ROOT_DIR/tests/validate_report.py" "$R1" \
         --deterministic-with "$R2"

echo "== exit code contract =="
set +e
"$BIN" --no-such-flag >/dev/null 2>&1
rc_usage=$?
"$BIN" --input /definitely/missing/media.mp4 --json >/dev/null 2>&1
rc_missing=$?
mkdir -p "$BROKEN_DIR"
head -c 4096 /dev/urandom > "$BROKEN"
"$BIN" --input "$BROKEN" --json >/dev/null 2>&1
rc_broken=$?
"$BIN" --input "$FIX" --json --output /no/such/directory/out.json >/dev/null 2>&1
rc_report=$?
set -e
for pair in "usage:$rc_usage:2" "missing:$rc_missing:2" \
            "broken:$rc_broken:3" "report:$rc_report:4"; do
    name="${pair%%:*}"
    rest="${pair#*:}"
    actual="${rest%%:*}"
    expected="${rest##*:}"
    if [[ "$actual" != "$expected" ]]; then
        echo "exit code mismatch for $name: got $actual want $expected" >&2
        exit 1
    fi
    echo "  $name exit code $actual OK"
done

echo "acceptance OK"
