#!/usr/bin/env bash
#
# clean.sh — remove build artifacts and local demo workspaces. Source and
# tracked files are never touched.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "removing build artifacts"
rm -f pack
find . -type f -name '*.test' -delete
find . -type f -name '*.out' -delete

echo "removing local demo workspaces"
rm -rf /tmp/game-pack-demo /tmp/game-pack-demo-idem /tmp/game-pack-demo-trunc \
  /tmp/trunc-demo /tmp/g2 /tmp/t2 /tmp/idem /tmp/verify-demo

echo "clean complete"
