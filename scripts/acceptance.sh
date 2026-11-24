#!/usr/bin/env bash
# macOS acceptance check: build, test, generate fixtures, verify, and prove
# checkpoint replay does not duplicate committed chunks.
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
  brew install go
fi

go mod tidy
go build ./...
go test ./...

rm -rf ./tmp/demo
go run ./cmd/mediapack demo --out ./tmp/demo --chunk-samples 8
go run ./cmd/mediapack verify --dir ./tmp/demo

manifest=./tmp/demo/manifest.json
hash_before=$(shasum -a 256 "$manifest" | awk '{print $1}')
last_checkpoint=$(find ./tmp/demo/index/checkpoints -maxdepth 1 -name '*.json' | sort | tail -1)
echo "deleting $last_checkpoint"
rm "$last_checkpoint"

go run ./cmd/mediapack recover --dir ./tmp/demo >/tmp/mediapack-recover.json
hash_after=$(shasum -a 256 "$manifest" | awk '{print $1}')
go run ./cmd/mediapack verify --dir ./tmp/demo

if [[ "$hash_before" != "$hash_after" ]]; then
  echo "manifest hash changed after checkpoint recovery" >&2
  echo "before=$hash_before" >&2
  echo "after =$hash_after" >&2
  exit 1
fi
echo "manifest hash stable: $hash_after"
