#!/usr/bin/env bash
# Full local acceptance for mediapack on macOS.
# Runs the documented commands, proves the recovery-only exit code 20 via the
# built binary (`go run` normalizes any non-zero child status to 1 while still
# printing exit_code=20), then exercises corruption + local rebuild scope.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WORK="./tmp/demo"
BIN_DIR="./tmp/bin"

echo "==> go mod tidy"
go mod tidy
echo "==> go build ./..."
go build ./...
echo "==> go test ./..."
go test ./...

rm -rf "$WORK" "./tmp/demo-bin" "$BIN_DIR"
mkdir -p "$BIN_DIR"

echo "==> demo with --crash-before-commit (via go run)"
set +e
go run ./cmd/mediapack demo --work "$WORK" --rates 2 --crash-before-commit \
  >./tmp/demo.out 2>./tmp/demo.err
GORUN_CODE=$?
set -e
echo "    go-run normalized exit=$GORUN_CODE (the go tool maps nonzero to 1)"
grep -q "simulated_crash=true exit_code=20" ./tmp/demo.out

echo "==> prove exact OS exit code 20 via built binary"
go build -o "$BIN_DIR/mediapack" ./cmd/mediapack
set +e
"$BIN_DIR/mediapack" demo --work ./tmp/demo-bin --rates 2 --crash-before-commit \
  >/dev/null 2>&1
BIN_CODE=$?
set -e
if [ "$BIN_CODE" -ne 20 ]; then
  echo "expected binary exit 20, got $BIN_CODE" >&2
  exit 1
fi
echo "    binary exit code = 20 OK"

echo "==> recover (expect recovered=1 duplicate_publish=0 missing_artifact=0)"
REC="$(go run ./cmd/mediapack recover --work "$WORK")"
echo "    $REC"
[ "$REC" = "recovered=1 duplicate_publish=0 missing_artifact=0" ]

echo "==> verify metrics"
VER="$(go run ./cmd/mediapack verify --work "$WORK")"
echo "$VER" | sed 's/^/    /'
echo "$VER" | grep -q "roundtrip_rate=1.000"
echo "$VER" | grep -q "keyframe_hit_rate=1.000"
echo "$VER" | grep -q "segment_hash_ok=8/8"
echo "$VER" | grep -q "boundary_align_error_packets=0"
echo "$VER" | grep -q "verify=PASS"

echo "==> corrupt one fragment, recover, prove only it is rebuilt"
find "$WORK/segments" -name '*.frag' | sort | xargs shasum -a 256 >./tmp/hash.before
go run ./cmd/mediapack corrupt --work "$WORK" --id 'r0/seg_0001' >/dev/null
set +e
REC2="$(go run ./cmd/mediapack recover --work "$WORK" 2>/dev/null)"
set -e
echo "    $REC2"
echo "$REC2" | grep -q "^recovered=1 duplicate_publish=0 missing_artifact=0$"
find "$WORK/segments" -name '*.frag' | sort | xargs shasum -a 256 >./tmp/hash.after
# Deterministic rebuild restores the corrupted file byte-for-byte, and every
# other fragment was never rewritten, so the whole hash set is identical.
diff ./tmp/hash.before ./tmp/hash.after >/dev/null
VER2="$(go run ./cmd/mediapack verify --work "$WORK")"
echo "$VER2" | grep -q "segment_hash_ok=8/8"
echo "$VER2" | grep -q "verify=PASS"

echo
echo "ACCEPTANCE PASS"
