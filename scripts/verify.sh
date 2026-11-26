#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "==> go mod tidy"
go mod tidy

echo "==> go build ./..."
go build ./...

echo "==> go test ./..."
go test ./...

echo "==> required package tests"
go test ./internal/ebml ./internal/task ./internal/manifest

WORK="${TMPDIR:-/tmp}/assetcheck-verify-$$"
mkdir -p "$WORK/pwf"
BIN="$WORK/assetcheck"
trap 'rm -rf "$WORK"' EXIT

echo "==> go build -o $BIN ./cmd/assetcheck"
go build -o "$BIN" ./cmd/assetcheck

check_line() {
  local file="$1"
  local expected="$2"
  if ! grep -Fqx "$expected" "$file"; then
    echo "missing expected line: $expected" >&2
    echo "--- output ---" >&2
    cat "$file" >&2
    exit 1
  fi
}

echo "==> clean"
"$BIN" demo --dir "$WORK/pwf" --case clean --redeliver 2 | tee "$WORK/clean.out"
check_line "$WORK/clean.out" "target_bps=768000"
check_line "$WORK/clean.out" "actual_bps=768000"
check_line "$WORK/clean.out" "delta_pct=0.00"
check_line "$WORK/clean.out" "psnr_db=120.00"
check_line "$WORK/clean.out" "align_max_ms=0.00"
CLEAN_SHA="$(sed -n 's/^manifest_sha256=//p' "$WORK/clean.out")"
[[ ${#CLEAN_SHA} -eq 64 ]]

for c in replace-identical metadata-change crash-resume unknown-size-eof; do
  echo "==> $c"
  "$BIN" demo --dir "$WORK/pwf" --case "$c" | tee "$WORK/$c.out"
  grep -q '^manifest_sha256=[0-9a-f]\{64\}$' "$WORK/$c.out"
done

grep -q '^index_rebuilt=true$' "$WORK/replace-identical.out"
grep -q '^cache_hit=true$' "$WORK/replace-identical.out"
grep -q "^manifest_sha256=$CLEAN_SHA$" "$WORK/replace-identical.out"
grep -q '^resume_from=parsed$' "$WORK/crash-resume.out"
grep -q "^manifest_sha256=$CLEAN_SHA$" "$WORK/crash-resume.out"

expect_exit_2() {
  local case_name="$1"
  local out="$WORK/$case_name.out"
  set +e
  "$BIN" demo --dir "$WORK/pwf" --case "$case_name" >"$out" 2>&1
  local code=$?
  set -e
  if [[ $code -ne 2 ]]; then
    echo "$case_name exit code = $code, want 2" >&2
    cat "$out" >&2
    exit 1
  fi
  grep -q 'offset=' "$out"
  grep -q 'path=/' "$out"
  echo "$case_name: exit=2 and location present"
}

expect_exit_2 invalid-vint
expect_exit_2 truncate-cluster

echo "VERIFY_OK work=$WORK"
