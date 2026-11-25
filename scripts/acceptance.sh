#!/usr/bin/env bash
# Reproducible macOS acceptance checks for rtptrace.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if ! command -v go >/dev/null 2>&1; then
  echo "go not found; install with: brew install go" >&2
  exit 64
fi

echo "== go mod tidy =="
go mod tidy
echo "== go build =="
go build ./...
echo "== go test =="
go test ./...

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/rtptrace"
go build -o "$BIN" ./cmd/rtptrace

NORMAL_ARGS=(demo --seed 17 --duration 120s --segment 6s --bitrates 800k,1600k --report json)

echo "== normal demo =="
go run ./cmd/rtptrace "${NORMAL_ARGS[@]}" > "$TMP/normal1.json"
go run ./cmd/rtptrace "${NORMAL_ARGS[@]}" > "$TMP/normal2.json"
cmp "$TMP/normal1.json" "$TMP/normal2.json"
grep -q '"byte_error": 0' "$TMP/normal1.json"
grep -q '"lost":' "$TMP/normal1.json"
grep -q '"duplicate":' "$TMP/normal1.json"
grep -q '"out_of_order":' "$TMP/normal1.json"
grep -Eq '"summary_sha256": "[0-9a-f]{64}"' "$TMP/normal1.json"
echo "normal summary: $(grep -m1 '"summary_sha256"' "$TMP/normal1.json" | tr -d ' ,')"

echo "== truncated-stbl =="
set +e
"$BIN" demo --seed 17 --fault truncated-stbl --report json > "$TMP/trunc1.json"
code=$?
set -e
[[ $code -eq 2 ]]
grep -q '"failure_track": 1' "$TMP/trunc1.json"
grep -q '"failure_sample": 37' "$TMP/trunc1.json"
grep -q '"recovery_state": "recovery_required"' "$TMP/trunc1.json"
"$BIN" demo --seed 17 --fault truncated-stbl --report json > "$TMP/trunc2.json" || true
cmp "$TMP/trunc1.json" "$TMP/trunc2.json"

echo "== edit-out-of-range --recover =="
set +e
"$BIN" demo --seed 17 --fault edit-out-of-range --recover --report json > "$TMP/edit1.json"
code=$?
set -e
[[ $code -eq 2 ]]
grep -q '"recovery_state": "recovered-edit-clamped"' "$TMP/edit1.json"
grep -q '"after"' "$TMP/edit1.json"
grep -q '"time_error_ticks": 48000' "$TMP/edit1.json"
"$BIN" demo --seed 17 --fault edit-out-of-range --recover --report json > "$TMP/edit2.json" || true
cmp "$TMP/edit1.json" "$TMP/edit2.json"

echo "ACCEPTANCE OK"
echo "Note: direct 'go run' wraps child exit status 2 as go's own status 1;"
echo "the compiled binary exits with the specified code 2."
