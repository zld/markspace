#!/usr/bin/env bash
# End-to-end demo: build the venv, install, simulate, and test.
set -euo pipefail

cd "$(dirname "$0")/.."

PYTHON="${PYTHON:-python3.11}"
VENV=".venv"

if [ ! -x "$VENV/bin/python" ]; then
  "$PYTHON" -m venv "$VENV"
fi

"$VENV/bin/python" -m pip install -q -e '.[test]'

"$VENV/bin/python" -m abr_engine simulate \
  --segments 120 \
  --seed 41 \
  --segment-seconds 4 \
  --bitrates 300,750,1200,1850,2600 \
  --initial-buffer 12 \
  --buffer-capacity 30 \
  --output text

"$VENV/bin/python" -m unittest discover -s tests
