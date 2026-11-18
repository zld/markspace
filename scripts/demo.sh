#!/bin/bash
# End-to-end demo: fixture generation, single-file analysis with gates,
# and a batch run that includes one corrupted WAV.
#
# Note: `go run` collapses any non-zero child exit to 1, so exit-code
# assertions use a freshly built binary; `go run` works for the
# success paths shown in the acceptance flow.
set -euo pipefail
cd "$(dirname "$0")/.."

WORK=./demo_out
rm -rf "$WORK"
mkdir -p "$WORK"
BIN="$WORK/loudcheck"
go build -o "$BIN" ./cmd/loudcheck

echo "== go test ./... =="
go test ./...

echo "== generate 5.1 fixture =="
go run ./cmd/loudcheck fixture --out "$WORK/sample.wav" --layout 5.1 --seconds 3

echo "== analyze with gates (expect pass, exit 0) =="
go run ./cmd/loudcheck analyze --input "$WORK/sample.wav" \
  --integrated-min -24 --integrated-max -16 --true-peak-max -1 --json \
  | tee "$WORK/analyze.json"

grep -q '"integrated_lufs": -' "$WORK/analyze.json"
grep -q '"short_term_max_lufs": -' "$WORK/analyze.json"
grep -q '"true_peak_dbtp": -' "$WORK/analyze.json"
grep -q '"loudness_pass": true' "$WORK/analyze.json"
grep -q '"true_peak_pass": true' "$WORK/analyze.json"
grep -q '"overall_pass": true' "$WORK/analyze.json"
grep -q '"channels": 6' "$WORK/analyze.json"
grep -q '"sample_rate": 48000' "$WORK/analyze.json"

echo "== batch with one corrupted WAV (expect exit 3) =="
cp "$WORK/sample.wav" "$WORK/good.wav"
head -c 200 "$WORK/sample.wav" > "$WORK/broken.wav"
printf '%s\n' "$WORK/good.wav" "$WORK/broken.wav" > "$WORK/list.txt"
set +e
"$BIN" batch --list "$WORK/list.txt" \
  --integrated-min -24 --integrated-max -16 --true-peak-max -1 --json \
  | tee "$WORK/batch.json"
code=${PIPESTATUS[0]}
set -e
if [ "$code" -ne 3 ]; then
  echo "expected exit 3 for batch with file errors, got $code" >&2
  exit 1
fi
grep -q '"failed": 1' "$WORK/batch.json"
grep -q '"succeeded": 1' "$WORK/batch.json"

echo "== gate failure (expect exit 1) =="
set +e
"$BIN" analyze --input "$WORK/sample.wav" --integrated-max -30 >/dev/null
code=$?
set -e
if [ "$code" -ne 1 ]; then
  echo "expected exit 1 for gate failure, got $code" >&2
  exit 1
fi

echo "== bad arguments (expect exit 2) =="
set +e
"$BIN" analyze --input "$WORK/sample.wav" --integrated-min -10 --integrated-max -20 >/dev/null 2>&1
code=$?
set -e
if [ "$code" -ne 2 ]; then
  echo "expected exit 2 for invalid gates, got $code" >&2
  exit 1
fi

echo "demo OK"
