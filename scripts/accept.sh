#!/usr/bin/env bash
# Reproduce the full offline RTP regression acceptance flow on macOS.
# Purely local: no network, no external media files. Fixtures are generated
# in-process by the tracecheck binary.
set -euo pipefail

cd "$(dirname "$0")/.."

OUT="${1:-/tmp/rtp-regression}"

echo "== go mod tidy =="
go mod tidy
echo "== go build ./... =="
go build ./...
echo "== go test ./... =="
go test ./...

echo "== build acceptance binary =="
BIN="$(mktemp -d)/tracecheck"
go build -o "$BIN" ./cmd/tracecheck

echo "== run demo -> $OUT =="
rm -rf "$OUT"
"$BIN" demo --out "$OUT"
echo
echo "demo exit code: $?"
echo "artifacts written under: $OUT"
