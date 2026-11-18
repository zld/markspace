#!/usr/bin/env bash
# End-to-end demo: build fixtures, run batch detection, query the database.
# Uses the project-local .venv exclusively; no system Python or network access.
set -euo pipefail

# Resolve the repository root regardless of the caller's working directory.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

PYTHON="${ROOT_DIR}/.venv/bin/python"
if [[ ! -x "${PYTHON}" ]]; then
  echo "error: project virtual environment missing at ${ROOT_DIR}/.venv" >&2
  echo "create it with: python3.11 -m venv .venv && .venv/bin/python -m pip install numpy==1.26.4" >&2
  exit 2
fi

DB="${ROOT_DIR}/demo.sqlite3"
FIXTURES="${ROOT_DIR}/demo-fixtures"

echo "==> 1/4 generating deterministic fixtures (seed 42)"
"${PYTHON}" -m audioslice.cli demo --db "${DB}" --out "${FIXTURES}" --seed 42

echo
echo "==> 2/4 batch detection (adaptive threshold, 180 ms silence, 90 ms merge)"
"${PYTHON}" -m audioslice.cli batch "${FIXTURES}" \
  --db "${DB}" --threshold-db -38 --min-silence-ms 180 --merge-gap-ms 90 --json

echo
echo "==> 3/4 stored results"
"${PYTHON}" -m audioslice.cli list --db "${DB}"

echo
echo "==> 4/4 aggregate summary"
"${PYTHON}" -m audioslice.cli summary --db "${DB}"

echo
echo "demo finished successfully (exit code 0 = all files succeeded)"
