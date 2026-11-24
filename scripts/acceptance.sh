#!/usr/bin/env bash
# Reproduces the local macOS acceptance record without external services.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

go mod tidy
go build ./...
go test ./...

DEMO=/tmp/mediarc-demo
BAD=/tmp/mediarc-bad
rm -rf "$DEMO" "$BAD"

demo_out="$(go run ./cmd/mediarc demo --seed 17 --out "$DEMO")"
echo "$demo_out"
grep -q 'roundtrip_rate=1\.000' <<<"$demo_out"
grep -q 'keyframe_hit_rate=1\.000' <<<"$demo_out"
grep -q 'segment_hash_match=4/4' <<<"$demo_out"

verify_out="$(go run ./cmd/mediarc verify --input "$DEMO")"
echo "$verify_out"
grep -q 'verification=ok' <<<"$verify_out"
grep -q 'segment_hash_match=4/4' <<<"$verify_out"

set +e
bad_out="$(go run ./cmd/mediarc demo --corrupt truncated-box --out "$BAD" 2>&1)"
bad_status=$?
set -e
echo "$bad_out"
if [[ $bad_status -eq 0 ]]; then
  echo "expected corruption command to fail" >&2
  exit 1
fi
grep -q 'byte_offset=' <<<"$bad_out"
if [[ -e "$BAD/manifest.json" ]]; then
  echo "corrupt run left a success-looking manifest" >&2
  exit 1
fi

late_out="$(go run ./cmd/mediarc demo --late-event --checkpoint "$DEMO/checkpoint.json")"
echo "$late_out"
grep -q 'recomputed=true' <<<"$late_out"
grep -q 'manifest_sha256_match=true' <<<"$late_out"

# `go run` can normalize a child's non-zero status to 1; verify the documented
# process exit-code contract with the compiled binary directly.
BIN=/tmp/mediarc-acceptance-bin
go build -o "$BIN" ./cmd/mediarc
set +e
"$BIN" definitely-invalid >/dev/null 2>&1
arg_status=$?
"$BIN" demo --corrupt truncated-box --out "${BAD}-binary" >/dev/null 2>&1
corrupt_status=$?
set -e
[[ $arg_status -eq 3 ]]
[[ $corrupt_status -eq 2 ]]

echo "acceptance=ok"
