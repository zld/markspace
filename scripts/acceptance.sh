#!/usr/bin/env bash
# Acceptance walk-through for a macOS Terminal.  Exits non-zero on the first
# failed check.  Everything runs locally; no network access is performed.
set -euo pipefail
cd "$(dirname "$0")/.."

step() { printf '\n== %s\n' "$1"; }

step "go mod tidy"
go mod tidy
git diff --exit-code -- go.mod go.sum >/dev/null || {
  echo "go.mod/go.sum changed; commit the tidy result first" >&2
  exit 1
}

step "build"
go build ./...

step "unit and end-to-end tests"
go test ./...

WORK="$(mktemp -d "${TMPDIR:-/tmp}/assetpack-accept.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

step "successful demo"
go run ./cmd/assetpack demo --work "$WORK/demo" \
  --target-rate 48000 --target-layout stereo --lufs -16

step "identical re-run must reuse everything"
go run ./cmd/assetpack demo --work "$WORK/demo" \
  --target-rate 48000 --target-layout stereo --lufs -16 \
  | tee "$WORK/rerun.txt"
grep -q 'added           : 0' "$WORK/rerun.txt"
grep -Eq 'reused          : 3' "$WORK/rerun.txt"

step "damaged hand-off"
set +e
go run ./cmd/assetpack demo --work "$WORK/broken" \
  --target-rate 48000 --target-layout stereo --lufs -16 \
  --inject truncated-ts,cc-jump,psi-update --fail-after manifest-stage \
  | tee "$WORK/broken.txt"
set -e
grep -q 'truncated-ts' "$WORK/broken.txt"
grep -q 'cc-jump' "$WORK/broken.txt"
grep -q 'psi-update' "$WORK/broken.txt"
grep -q 'rollback verified' "$WORK/broken.txt"
grep -q 'result          : OK' "$WORK/broken.txt"

step "manifest verification"
go run ./cmd/assetpack verify --work "$WORK/broken"

printf '\nacceptance passed\n'
