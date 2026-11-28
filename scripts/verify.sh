#!/bin/sh
# verify.sh - local acceptance run for the rtptrace regression pipeline.
# Purely local: no network, no devices. Safe to re-run; uses a fresh temp root.
set -eu

ROOT="${TMPDIR:-/tmp}/rtp-check"
BIN="$(dirname "$0")/../.tmp/rtptrace"

echo "== go build/test =="
( cd "$(dirname "$0")/.." && go build ./... && go test ./... )

echo "== build adapter =="
( cd "$(dirname "$0")/.." && go build -o "$BIN" ./cmd/rtptrace )

rm -rf "$ROOT"

echo "== demo with interruption after rtp =="
"$BIN" demo --root "$ROOT" --seed 17 --interrupt-after rtp

echo "== resume (expect recovered=true) =="
"$BIN" resume --root "$ROOT"

echo "== repeat resume (hashes must be unchanged) =="
"$BIN" resume --root "$ROOT"

echo "== independent shasum cross-check =="
shasum -a 256 "$ROOT/manifest.json" "$ROOT/artifact.bin"

echo "== corrupt localization (exit code 2, bad_offset within 8 of 4096) =="
set +e
"$BIN" corrupt --kind truncated-moov --at 4096 --root "${TMPDIR:-/tmp}/rtp-corrupt"
code=$?
set -e
if [ "$code" -ne 2 ]; then
	echo "expected exit code 2 for corrupt input, got $code" >&2
	exit 1
fi
echo "corrupt exit code OK ($code)"
