#!/usr/bin/env bash
# End-to-end acceptance for the replay slicing library/demo.
#
# Mirrors the documented acceptance procedure and additionally checks:
#   - exit code 2 for parameter errors, 1 for input-constraint failure,
#   - identical manifest hashes on identical re-runs,
#   - manifest hash returns to the clean value after a decidable repair.
#
# Usage: scripts/acceptance.sh [fixture-dir]
# Requires: go 1.23+ in PATH, ffmpeg/ffprobe in PATH.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
FIXTURE="${1:-/tmp/replay-fixture}"
BIN="$(mktemp -t replay-demo.XXXXXX)"
trap 'rm -f "$BIN"' EXIT

echo "==> go mod tidy / build / test"
go mod tidy
go build -o "$BIN" ./cmd/demo
go test ./...

echo "==> generate fixture at $FIXTURE"
rm -rf "$FIXTURE"
"$BIN" --generate --out "$FIXTURE"

echo "==> documented acceptance run"
"$BIN" --input "$FIXTURE" --event kill-1 \
  --window-before 3s --window-after 5s \
  --target-bitrate 1800000 --workers 2 --verify

hash_of() {
  sed -n 's/.*"manifest_hash": "\([0-9a-f]\{64\}\)".*/\1/p' "$1"
}
MANIFEST="$FIXTURE/out/normal/kill-1.manifest.json"
H_CLEAN="$(hash_of "$MANIFEST")"
[ ${#H_CLEAN} -eq 64 ] || { echo "bad manifest hash: $H_CLEAN"; exit 1; }

echo "==> determinism: identical re-run keeps $H_CLEAN"
"$BIN" --input "$FIXTURE" --event kill-1 \
  --window-before 3s --window-after 5s \
  --target-bitrate 1800000 --workers 2 >/dev/null
H_RERUN="$(hash_of "$MANIFEST")"
[ "$H_RERUN" = "$H_CLEAN" ] || { echo "hash changed on re-run"; exit 1; }

echo "==> repair: duplicate seq file then remove it restores the hash"
cp "$FIXTURE/normal/seg_02.m4s" "$FIXTURE/normal/seg_02_dup.m4s"
"$BIN" --input "$FIXTURE" --event kill-1 --workers 2 >/dev/null
H_DUP="$(hash_of "$MANIFEST")"
[ "$H_DUP" != "$H_CLEAN" ] || { echo "dup must change issue-count hash"; exit 1; }
rm "$FIXTURE/normal/seg_02_dup.m4s"
"$BIN" --input "$FIXTURE" --event kill-1 --workers 2 >/dev/null
H_FIXED="$(hash_of "$MANIFEST")"
[ "$H_FIXED" = "$H_CLEAN" ] || { echo "hash not restored after repair"; exit 1; }

echo "==> exit codes"
set +e
"$BIN" --bogus >/dev/null 2>&1; rc=$?; [ $rc -eq 2 ] || { echo "bad flag rc=$rc want 2"; exit 1; }
"$BIN" --input "$FIXTURE" >/dev/null 2>&1; rc=$?; [ $rc -eq 2 ] || { echo "missing event rc=$rc want 2"; exit 1; }
"$BIN" --input "$FIXTURE/audio-unsupported" --event kill-1 --workers 2 >/dev/null 2>&1
rc=$?; [ $rc -eq 1 ] || { echo "unsupported set rc=$rc want 1"; exit 1; }
printf 'TAMPER-BYTES' >> "$FIXTURE/out/normal/kill-1.mp4"
"$BIN" --input "$FIXTURE" --event kill-1 --workers 2 --verify >/dev/null 2>&1
rc=$?; [ $rc -eq 1 ] || { echo "tampered verify rc=$rc want 1"; exit 1; }
set -e

echo
echo "ALL ACCEPTANCE CHECKS PASSED"
echo "manifest hash: $H_CLEAN"
