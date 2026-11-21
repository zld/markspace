#!/usr/bin/env bash
# verify.sh - build streamcheck, generate fixtures, and check every case.
#
# Usage: scripts/verify.sh [fixture-dir]
# Exit codes follow the CLI contract: 0 pass, 1 inconsistency, 2 setup error.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FIXTURE_DIR="${1:-$(mktemp -d "${TMPDIR:-/tmp}/hls-fixture.XXXXXX")}"
BIN="$(mktemp -d "${TMPDIR:-/tmp}/streamcheck-bin.XXXXXX")/streamcheck"
TMP_OUT="$(mktemp -d "${TMPDIR:-/tmp}/streamcheck-out.XXXXXX")"
trap 'rm -rf "$(dirname "$BIN")" "$TMP_OUT"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}
setup_fail() {
  echo "ERROR: $*" >&2
  exit 2
}

# expect_exit WANT LABEL -- command...
expect_exit() {
  local want="$1" label="$2"
  shift 3
  "$@" >"$TMP_OUT/out.txt" 2>"$TMP_OUT/err.txt"
  local got=$?
  if [[ "$got" -ne "$want" ]]; then
    echo "--- stdout ---"; cat "$TMP_OUT/out.txt"
    echo "--- stderr ---"; cat "$TMP_OUT/err.txt"
    fail "$label: exit $got, want $want"
  fi
  echo "ok   $label (exit $got)"
}

echo "== toolchain =="
command -v go >/dev/null 2>&1 || setup_fail "go not found in PATH"
command -v ffprobe >/dev/null 2>&1 || setup_fail "ffprobe not found; run: brew install ffmpeg"
echo "go:      $(go version)"
echo "ffprobe: $(ffprobe -version 2>/dev/null | head -n 1)"

echo "== go vet / go test =="
(cd "$ROOT" && go vet ./...) || setup_fail "go vet failed"
(cd "$ROOT" && go test ./...) || setup_fail "go test failed"

echo "== build =="
(cd "$ROOT" && go build -o "$BIN" ./cmd/streamcheck) || setup_fail "go build failed"

echo "== fixture =="
rm -rf "$FIXTURE_DIR"
expect_exit 0 "fixture generation" -- "$BIN" fixture --out "$FIXTURE_DIR"

echo "== happy path (JSON) =="
expect_exit 0 "happy path" -- "$BIN" build --input "$FIXTURE_DIR" \
  --rates 800,1600 --segment-seconds 4 --tolerance-ms 80 --json
cp "$TMP_OUT/out.txt" "$TMP_OUT/normal.json"

python3 - "$TMP_OUT/normal.json" <<'PY' || fail "happy-path JSON assertions failed"
import json, sys
doc = json.load(open(sys.argv[1]))
assert len(doc["rates"]) == 2, doc["rates"]
for rate in doc["rates"]:
    assert rate["segment_count"] == 6, rate
    assert 23.9 <= rate["total_duration_seconds"] <= 24.1, rate
    assert rate["duration_delta_ms"] <= 80, rate
    assert rate["validation_failures"] == [], rate
    assert rate["missing_ranges"] == [] and rate["duplicate_ids"] == [], rate
    assert len(rate["manifest_sha256"]) == 64, rate
print("ok   happy-path JSON contract")
PY

echo "== anomaly cases =="
expect_exit 0 "missing (lenient)" -- "$BIN" build --input "$FIXTURE_DIR/cases/missing" --rates 800,1600 --json
expect_exit 1 "missing (strict)" -- "$BIN" build --input "$FIXTURE_DIR/cases/missing" --rates 800,1600 --strict --json
expect_exit 1 "duplicate" -- "$BIN" build --input "$FIXTURE_DIR/cases/duplicate" --rates 800,1600 --json
expect_exit 0 "unordered" -- "$BIN" build --input "$FIXTURE_DIR/cases/unordered" --rates 800,1600 --json
expect_exit 1 "drift" -- "$BIN" build --input "$FIXTURE_DIR/cases/drift" --rates 800,1600 --tolerance-ms 80 --json

echo "== usage errors =="
expect_exit 2 "duplicate rates" -- "$BIN" build --input "$FIXTURE_DIR" --rates 800,800
expect_exit 2 "negative tolerance" -- "$BIN" build --input "$FIXTURE_DIR" --rates 800 --tolerance-ms -1
expect_exit 2 "missing input" -- "$BIN" build --input "$TMP_OUT/nope" --rates 800
expect_exit 2 "no subcommand" -- "$BIN"

echo "== manifest and idempotence =="
"$BIN" build --input "$FIXTURE_DIR" --rates 800,1600 --json >"$TMP_OUT/again.json" \
  || fail "second build failed"
python3 - "$TMP_OUT/normal.json" "$TMP_OUT/again.json" <<'PY' || fail "rebuild is not reproducible"
import json, sys
a, b = (json.load(open(p)) for p in sys.argv[1:])
assert [r["manifest_sha256"] for r in a["rates"]] == [r["manifest_sha256"] for r in b["rates"]], (a, b)
print("ok   manifest digests stable across runs")
PY

grep -q '^#EXTM3U$' "$FIXTURE_DIR/media_800.m3u8" || fail "manifest header missing"
grep -q '^#EXT-X-ENDLIST$' "$FIXTURE_DIR/media_800.m3u8" || fail "manifest ENDLIST missing"
grep -Eq '^#EXTINF:[0-9]+\.[0-9]{3},$' "$FIXTURE_DIR/media_800.m3u8" || fail "EXTINF precision is not milliseconds"
echo "ok   manifest structure"

echo
echo "ALL CHECKS PASSED"
