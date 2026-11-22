#!/bin/sh
# Acceptance run: build, test, demo (twice for idempotency) and fixtures.
set -e
cd "$(dirname "$0")/.."

go mod tidy
go build ./...
go test ./...

OUT=${1:-/tmp/mediapack-demo}
rm -rf "$OUT"

go run ./cmd/mediapack demo --seed 17 --out "$OUT" --resume | tee /tmp/mediapack-run1.txt
sha256sum "$OUT/artifacts/manifest.json" > /tmp/mediapack-run1.sums

go run ./cmd/mediapack demo --seed 17 --out "$OUT" --resume | tee /tmp/mediapack-run2.txt
sha256sum "$OUT/artifacts/manifest.json" > /tmp/mediapack-run2.sums

diff /tmp/mediapack-run1.sums /tmp/mediapack-run2.sums
echo "idempotency: manifest digests identical"

go run ./cmd/mediapack verify-fixtures
echo "acceptance: OK"
