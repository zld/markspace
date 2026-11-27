#!/usr/bin/env bash
# End-to-end verification for the mediad local audio processing kernel.
# Runs entirely locally on macOS with no network services or GUI required.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "== go version =="
go version

echo "== go mod tidy =="
go mod tidy

echo "== go build ./... =="
go build ./...

echo "== go test ./... =="
go test ./...

echo "== demo (acceptance command) =="
WORK_DIR="$(mktemp -d)/mediad-work"
BIN="$(mktemp -d)/mediad"
# Build a real binary so process exit codes are observed directly (go run
# wraps non-zero exits into 1).
go build -o "$BIN" ./cmd/mediad
# The demo intentionally contains a truncated/corrupt sample, so per the
# contract it exits 3 (input corruption) while still printing the full JSON.
set +e
"$BIN" demo \
  --seed 42 \
  --budget-ms 5000 \
  --max-bytes 65536 \
  --target-rate 48000 \
  --layout stereo \
  --lufs -16 \
  --true-peak -1.0 \
  --work-dir "$WORK_DIR"
code=$?
set -e
echo "demo exit code: $code (3 expected: corrupt input sample is isolated)"
if [ "$code" -ne 3 ]; then
  echo "unexpected exit code $code" >&2
  exit 1
fi
echo "OK"
