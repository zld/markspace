#!/usr/bin/env bash
# End-to-end demo: generate a trace, run the simulation, run the tests.
set -euo pipefail
cd "$(dirname "$0")/.."

PY=.venv/bin/python
TRACE=/tmp/abr-trace.json

echo "== generate trace (seed 41, 120 segments) =="
$PY scripts/generate_trace.py --seed 41 --segments 120 --output "$TRACE"

echo "== simulate: JSON Lines output =="
$PY -m abr_engine.cli simulate --trace "$TRACE" --segments 120 \
  --segment-duration 2 --bitrates 350000,700000,1200000,2000000 \
  --initial-buffer 4 --max-buffer 20 --output jsonl

echo "== simulate: table output, EMA estimator, tighter hysteresis =="
$PY -m abr_engine.cli simulate --trace "$TRACE" --segments 120 \
  --segment-duration 2 --bitrates 350000,700000,1200000,2000000 \
  --initial-buffer 4 --max-buffer 20 \
  --estimator ema --alpha 0.4 --hysteresis 2 --output table

echo "== unit tests =="
$PY -m unittest discover -s tests -v
