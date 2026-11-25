#!/usr/bin/env bash
# End-to-end acceptance driver for regress-kernel. Purely local, no services.
set -euo pipefail

cd "$(dirname "$0")/.."

BIN="$(mktemp -d)/regress"
go build -o "$BIN" ./cmd/regress

echo "== tidy / build / test =="
go mod tidy
go build ./...
go test ./...

rm -rf /tmp/regress-a /tmp/regress-b /tmp/regress-recover

echo "== normal run A =="
"$BIN" -case normal -seed 17 -out /tmp/regress-a
echo "== normal run B =="
"$BIN" -case normal -seed 17 -out /tmp/regress-b

sha_a=$(shasum -a 256 /tmp/regress-a/manifest.json | awk '{print $1}')
sha_b=$(shasum -a 256 /tmp/regress-b/manifest.json | awk '{print $1}')
if [ "$sha_a" != "$sha_b" ]; then
  echo "FAIL: manifest SHA-256 differs across normal runs" >&2
  exit 1
fi
echo "manifest stable: $sha_a"

echo "== corrupt (real exit code must be 2) =="
set +e
"$BIN" -case corrupt -seed 17
code=$?
set -e
if [ "$code" -ne 2 ]; then
  echo "FAIL: corrupt exit code $code, want 2" >&2
  exit 1
fi
echo "corrupt exited 2 as expected"

echo "== crash-after-publish: first run =="
"$BIN" -case crash-after-publish -seed 17 -out /tmp/regress-recover || true
seg_after_crash=$(find /tmp/regress-recover/segments -name '*.m4s' | wc -l | tr -d ' ')

echo "== crash-after-publish: recovery run =="
"$BIN" -case crash-after-publish -seed 17 -out /tmp/regress-recover
seg_after_recover=$(find /tmp/regress-recover/segments -name '*.m4s' | wc -l | tr -d ' ')
if [ "$seg_after_crash" != "$seg_after_recover" ]; then
  echo "FAIL: artifact count changed $seg_after_crash -> $seg_after_recover" >&2
  exit 1
fi
echo "artifact count stable: $seg_after_recover segments"
echo "ALL ACCEPTANCE CHECKS PASSED"
