#!/bin/bash
# Reproducible demo: create venv, install, test, generate and simulate.
set -euo pipefail
cd "$(dirname "$0")/.."

python3.11 -m venv .venv
.venv/bin/python -m pip install --upgrade pip
.venv/bin/python -m pip install -e .
.venv/bin/python -m unittest discover -s tests -v
.venv/bin/python -m abr_engine.cli generate --seed 41 --duration 180 --interval 1 --output /tmp/abr-trace.json
.venv/bin/python -m abr_engine.cli simulate --trace /tmp/abr-trace.json \
  --bitrates 300000,750000,1200000,2400000,4000000 \
  --segments 60 --segment-duration 4 --initial-buffer 8 --json
