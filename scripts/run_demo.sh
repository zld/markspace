#!/usr/bin/env bash
# Reproduce the acceptance demo from a clean checkout.
# Requires the project virtualenv created per the README:
#   "$(brew --prefix python@3.11)/bin/python3.11" -m venv .venv
#   .venv/bin/python -m pip install -e .
set -euo pipefail

cd "$(dirname "$0")/.."

.venv/bin/python -m trackmix demo \
  --seed 23 \
  --tracks 4 \
  --frames 96000 \
  --rate 48000 \
  --block-frames 257 \
  --automation-step 12000 \
  --stress-clip \
  --verify-stream

.venv/bin/python -m unittest discover -s tests
