#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Lock/download dependencies, build, and run every module test.
go mod tidy
go build -o /tmp/continuity ./cmd/continuity
go test ./...

# Expected-success verification scenarios.
/tmp/continuity verify --case clean --out /tmp/continuity-clean
/tmp/continuity verify --case damaged
/tmp/continuity verify --case boundary
/tmp/continuity verify --case recovery --out /tmp/continuity-recovery

# Explicit terminal exit-code contract for direct invocation.
set +e
/tmp/continuity run --case damaged --out /tmp/continuity-exit-damaged >/dev/null 2>&1
damaged_code=$?
/tmp/continuity run --case recovery --out /tmp/continuity-exit-crash --inject-crash >/dev/null 2>&1
crash_code=$?
set -e

[[ "$damaged_code" -eq 2 ]] || { echo "expected input exit 2, got $damaged_code"; exit 1; }
[[ "$crash_code" -eq 75 ]] || { echo "expected crash exit 75, got $crash_code"; exit 1; }
echo "acceptance_script=pass direct_input_exit=2 injected_crash_exit=75"
