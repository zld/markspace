#!/usr/bin/env bash
# End-to-end demo for LadderForge.
#
# Prerequisites (macOS):
#   brew install python@3.11 ffmpeg
#   python3.11 -m venv .venv
#   .venv/bin/python -m pip install numpy==2.2.6
#
# Run from the project root:  bash scripts/demo.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PY=".venv/bin/python"
DB=".runs/demo.sqlite"
COMMON=(
  --db "$DB"
  --frames 48
  --resolutions 426x240,640x360
  --bitrates-kbps 250,500,1000,1000
  --presets ultrafast,medium
  --workers 2
)

if [[ ! -x "$PY" ]]; then
  echo "error: $PY not found; create the venv first (see header)" >&2
  exit 1
fi

echo "==> compile"
PYTHONPYCACHEPREFIX=/tmp/ladderforge-pycache "$PY" -m compileall -q src tests

echo "==> tests"
PYTHONPYCACHEPREFIX=/tmp/ladderforge-pycache PYTHONPATH=src \
  "$PY" -m unittest discover -s tests

echo "==> first pass (limited to 3 jobs)"
rm -rf .runs
PYTHONPATH=src "$PY" -m ladderforge run "${COMMON[@]}" --limit-jobs 3

echo "==> resume to completion"
PYTHONPATH=src "$PY" -m ladderforge run "${COMMON[@]}" --resume

echo
echo "Artifacts:"
echo "  database: $DB"
echo "  sources/encodes: .runs/work/"
