#!/bin/sh
# verify.sh - end-to-end acceptance for the media processing kernel.
#
# It runs the deterministic demo twice and checks the outputs are byte
# identical, then exercises truncated, corrupted, rollover and
# out-of-window-late fixtures plus an interrupt/resume scenario through the
# `mediapipeline verify` command. Exit status is 0 only when every check
# passes; any input, sync, dependency or checksum failure makes it non-zero.
set -eu

# Resolve the repository root from this script's location so it works from
# any current working directory.
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
cd "$ROOT"

WORKDIR="${TMPDIR:-/tmp}/mediapipeline-verify-$$"
OUT_A="$WORKDIR/media-a.mp4"
OUT_B="$WORKDIR/media-b.mp4"
OUT_V="$WORKDIR/media-v.mp4"
mkdir -p "$WORKDIR"
trap 'rm -rf "$WORKDIR"' EXIT

echo "== go build =="
go build ./...

echo "== go test =="
go test ./...

echo "== demo run A =="
go run ./cmd/mediapipeline demo --seed 7 --out "$OUT_A"

echo "== demo run B =="
go run ./cmd/mediapipeline demo --seed 7 --out "$OUT_B"

HASH_A=$(shasum -a 256 "$OUT_A" | awk '{print $1}')
HASH_B=$(shasum -a 256 "$OUT_B" | awk '{print $1}')
if [ "$HASH_A" != "$HASH_B" ]; then
	echo "FAIL: demo digests differ ($HASH_A vs $HASH_B)" >&2
	exit 1
fi
echo "demo sha256=$HASH_A (identical across runs)"

echo "== fault injection and resume =="
go run ./cmd/mediapipeline verify --workdir "$WORKDIR/verify" --out "$OUT_V"

# The verifier enforces the <=1 byte corruption localization, the resume
# timing bound and digest equality internally; reaching here means they held.
echo "PASS: all acceptance checks succeeded"
