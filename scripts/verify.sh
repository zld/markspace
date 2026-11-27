#!/usr/bin/env bash
#
# verify.sh — full local acceptance run on macOS. No external services.
# Usage: scripts/verify.sh [DEMO_ROOT]
set -euo pipefail

ROOT="${1:-/tmp/game-pack-demo}"
PACK="$(cd "$(dirname "$0")/.." && pwd)/pack"

echo "== go build ./... =="
go build ./...

echo "== go vet ./... =="
go vet ./...

echo "== go test ./... =="
go test ./...

echo "== build pack binary =="
go build -o "$PACK" ./cmd/pack

echo
echo "== demo: crash after align (switch/wrap/truncated) =="
rm -rf "$ROOT"
"$PACK" demo --root "$ROOT" \
  --codecs h264,h265,av1 --bitrates 500,1000 \
  --inject switch,wrap,truncated --crash-after align || true

echo
echo "== resume =="
"$PACK" resume --root "$ROOT"

echo
echo "== verify (expect exit 0) =="
"$PACK" verify --root "$ROOT"

echo
echo "== truncated-only negative job (expect exit 2, no partial commit) =="
rm -rf "${ROOT}-trunc"
if "$PACK" demo --root "${ROOT}-trunc" --codecs h264 --bitrates 500 \
    --inject truncated; then
  echo "ERROR: truncated job unexpectedly succeeded" >&2
  exit 1
fi
test ! -e "${ROOT}-trunc/manifest.json"
echo "OK: no partial commit"

echo
echo "== idempotent rerun on a clean root (expect new=0, reused=all) =="
IDEM_ROOT="${ROOT}-idem"
rm -rf "$IDEM_ROOT"
"$PACK" demo --root "$IDEM_ROOT" --codecs h264,h265,av1 \
  --bitrates 500,1000 --inject switch,wrap >/dev/null
"$PACK" demo --root "$IDEM_ROOT" --codecs h264,h265,av1 \
  --bitrates 500,1000 --inject switch,wrap | grep -E 'reused|new'

echo
echo "ALL ACCEPTANCE CHECKS PASSED"
