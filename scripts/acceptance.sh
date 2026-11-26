#!/usr/bin/env bash
# Reproduce the local mediaprep acceptance workflow on macOS.
# Note: "go run" reports "exit status N" but its own shell status is 1 for any
# nonzero child status; the compiled binary preserves application codes 0/2/3.
set -euo pipefail

cd "$(dirname "$0")/.."
if [ -x /opt/homebrew/opt/go@1.23/bin/go ]; then
  export PATH="/opt/homebrew/opt/go@1.23/bin:$PATH"
fi

ROOT="${MP_ROOT:-$(mktemp -d)/mp-demo}"
VERIFY_ROOT="${MP_VERIFY_ROOT:-$(mktemp -d)/mp-verify}"
mkdir -p "$ROOT" "$VERIFY_ROOT"

go version
go mod download
go mod tidy
go build ./...
go test ./...

go run ./cmd/mediaprep demo \
  --generate --root "$ROOT" --queue 2 --budget-bytes 262144 --slow-consumer-ms 5

go run ./cmd/mediaprep verify \
  --generate --root "$VERIFY_ROOT" \
  --cases truncated-klv,box-overrun,crop-outside,color-conflict,cancel

BIN="$ROOT/mediaprep"
go build -o "$BIN" ./cmd/mediaprep

POINTER="$ROOT/manifest/current.json"
BEFORE="$(shasum -a 256 "$POINTER" | awk '{print $1}')"
set +e
"$BIN" migrate --root "$ROOT" --from v1 --stop-after prepare
PREPARE_CODE=$?
set -e
if [ "$PREPARE_CODE" -ne 3 ]; then
  echo "expected prepare exit 3, got $PREPARE_CODE" >&2
  exit 1
fi
AFTER="$(shasum -a 256 "$POINTER" | awk '{print $1}')"
test "$BEFORE" = "$AFTER"

"$BIN" migrate --root "$ROOT" --resume
grep -q '"version": "v2"' "$POINTER"
test -f "$ROOT/manifest/rollback-v1.json"

set +e
"$BIN" inspect --file "$VERIFY_ROOT/truncated-klv.mxf" >/dev/null 2>&1
CORRUPT_CODE=$?
set -e
if [ "$CORRUPT_CODE" -ne 2 ]; then
  echo "expected corrupt input exit 2, got $CORRUPT_CODE" >&2
  exit 1
fi

echo "acceptance=PASS root=$ROOT"
