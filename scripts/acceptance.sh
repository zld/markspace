#!/usr/bin/env bash
# Reproducible local acceptance runner for mediad.
# Usage: scripts/acceptance.sh [workdir]
set -euo pipefail

OUT="${1:-/tmp/mediad-accept}"
rm -rf "$OUT"
mkdir -p "$OUT"

echo "== go mod download =="
go mod download
echo "== go build =="
go build ./...
echo "== go test =="
go test ./...

BIN="$OUT/mediad"
go build -o "$BIN" ./cmd/mediad

echo "== clean demo =="
"$BIN" demo --out "$OUT/clean"

echo "== corrupt sync (expected non-zero) =="
set +e
"$BIN" demo --corrupt sync --out "$OUT/bad"
rc=$?
set -e
if [ "$rc" -ne 1 ]; then
  echo "FAIL: corrupt run exit $rc, want 1" >&2
  exit 1
fi

echo "== interrupt + resume =="
"$BIN" demo --interrupt-after segment:2 --out "$OUT/resume"
"$BIN" resume --out "$OUT/resume"

echo "== verify clean and resumed =="
"$BIN" verify --out "$OUT/clean"
"$BIN" verify --out "$OUT/resume"

# The resumed manifest must be byte-digest identical to a full run.
sha_full=$(python3 -c "import json;print(json.load(open('$OUT/clean/manifest.json'))['manifest_sha256'])")
sha_res=$(python3 -c "import json;print(json.load(open('$OUT/resume/manifest.json'))['manifest_sha256'])")
if [ "$sha_full" != "$sha_res" ]; then
  echo "FAIL: manifest SHA mismatch $sha_full vs $sha_res" >&2
  exit 1
fi
echo "ACCEPTANCE OK: $sha_full"
