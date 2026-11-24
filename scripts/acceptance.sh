#!/usr/bin/env bash
#
# Reproduce the full mediascan acceptance matrix locally on macOS.
# Builds a real binary so process exit codes match the documented contract
# (go run would translate a child exit code of 2 into 1).
set -u

cd "$(dirname "$0")/.."

echo "== go mod tidy =="
go mod tidy

echo "== go build ./... =="
go build ./... || exit 1

echo "== go test ./... =="
go test ./... || exit 1

BIN="$(mktemp -d)/mediascan"
echo "== building acceptance binary: $BIN =="
go build -o "$BIN" ./cmd/mediascan || exit 1

run() {
  local name="$1"; shift
  echo
  echo "== CASE: $name =="
  "$BIN" "$@"
  echo "   (exit code: $?)"
}

run "normal"   --generate --case normal --format hls,dash
run "damaged"  --generate --case damaged --truncate-box 17 \
               --timestamp-wrap --vfr --drift-ppm 120
run "boundary" --generate --case boundary \
               --duplicate-sequence --missing-segment
run "resume"   --generate --case damaged --resume

echo
echo "All acceptance cases executed. Inspect exit codes above:"
echo "  normal/resume-consistency -> 0 or 2 only for the underlying damaged material"
echo "  damaged/boundary          -> 2"
echo "  bad arguments             -> 1"
