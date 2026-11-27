#!/usr/bin/env bash
#
# acceptance.sh — run the full acceptance sequence from a clean state and
# assert exit codes and headline metrics. Intended for macOS Terminal.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

OUT="$(mktemp -d)/run"
CORRUPT="$(mktemp -d)/corrupt"
BOUNDARY="$(mktemp -d)/boundary"

echo "== go mod tidy =="
go mod tidy
echo "== go build =="
go build ./...
echo "== go test =="
go test ./...

echo "== demo with interrupt =="
go run ./cmd/continuity demo --out "$OUT" --interrupt-after 5

echo "== resume =="
go run ./cmd/continuity resume --out "$OUT" | tee resume.txt
grep -q "frames=120" resume.txt
grep -q "bitrate_error_pct=0.0000" resume.txt
grep -q "manifest_alignment_ns=0" resume.txt

echo "== verify =="
go run ./cmd/continuity verify --out "$OUT"

echo "== corrupt checksum must fail non-zero =="
if go run ./cmd/continuity demo --out "$CORRUPT" --corrupt checksum; then
  echo "corrupt case unexpectedly succeeded" >&2
  exit 1
fi

echo "== timestamp overflow must fail non-zero =="
if go run ./cmd/continuity demo --out "$BOUNDARY" --case timestamp-overflow; then
  echo "overflow case unexpectedly succeeded" >&2
  exit 1
fi

rm -f resume.txt
echo "ALL ACCEPTANCE CHECKS PASSED"
