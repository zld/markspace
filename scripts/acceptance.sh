#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT"

WORK="$ROOT/tmp/acceptance"
rm -rf "$WORK"

echo "== go mod tidy/check =="
go mod tidy

echo "== build =="
"$SCRIPT_DIR/build.sh"

echo "== tests =="
go test ./...

echo "== demo =="
go run ./cmd/mediacore demo --work "$WORK"

echo "== verify =="
go run ./cmd/mediacore verify --work "$WORK"

BIN="$ROOT/tmp/bin/mediacore"
check_code() {
  local name="$1" want="$2"
  set +e
  "$BIN" verify --work "$WORK" --case "$name" >/dev/null 2>&1
  local got=$?
  set -e
  if [[ "$got" != "$want" ]]; then
    echo "case $name exit code=$got, want $want" >&2
    exit 1
  fi
  echo "exit-code case $name: $got"
}

check_code truncated-shard 2
check_code illegal-sample 2
check_code zero-timescale 2
check_code time-regression 2
check_code changed-input 3

echo "ACCEPTANCE PASS"
