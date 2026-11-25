#!/usr/bin/env bash
# Run the documented local acceptance flow on macOS.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

NORMAL="${TMPDIR:-/tmp}/game-pack-acceptance"
BAD="${TMPDIR:-/tmp}/game-pack-bad-acceptance"
BIN="${TMPDIR:-/tmp}/mediapack-acceptance-bin"
rm -rf "$NORMAL" "$BAD" "$BIN" "${BAD}-code"

echo "==> go mod tidy"
go mod tidy

echo "==> go build ./..."
go build ./...

echo "==> go test ./..."
go test ./...

echo "==> normal demo"
go run ./cmd/pack demo --out "$NORMAL" > "$NORMAL.first.json"
required=(input_sha256 reused_assets rebuilt_assets segment_sha256 pixel_mean peak_dbfs alignment_error_ms)
for key in "${required[@]}"; do
  python3 - "$NORMAL.first.json" "$key" <<'PY'
import json, sys
path, key = sys.argv[1], sys.argv[2]
with open(path) as f:
    data = json.load(f)
assert key in data, f"missing {key}"
PY
done

first_hash="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["segment_sha256"])' "$NORMAL.first.json")"
go run ./cmd/pack demo --out "$NORMAL" > "$NORMAL.second.json"
second_hash="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["segment_sha256"])' "$NORMAL.second.json")"
rebuilt="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["rebuilt_assets"])' "$NORMAL.second.json")"
[[ "$rebuilt" == "0" ]] || { echo "expected rebuilt_assets=0, got $rebuilt"; exit 1; }
[[ "$first_hash" == "$second_hash" ]] || { echo "segment hash changed on rerun"; exit 1; }
echo "    deterministic segment_sha256=$second_hash"

echo "==> corrupt boundary via go run"
set +e
go run ./cmd/pack demo --corrupt-boundary --out "$BAD" > "$BAD.json" 2> "$BAD.go-run.stderr"
go_run_code=$?
set -e
# go run intentionally wraps a nonzero child as its own status 1 and prints
# "exit status 2"; the JSON failure record carries the semantic exit code 2.
python3 - "$BAD.json" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
f = r["failure"]
assert f["exit_code"] == 2, f
assert f["layer"], "missing layer"
assert f["segment"] == 2, f
assert f["kind"] == "boundary_mismatch", f
assert f["offset_ms"] > 40, f
print(f"    reported {f['layer']} segment={f['segment']} offset_ms={f['offset_ms']}")
PY
grep -q "exit status 2" "$BAD.go-run.stderr"
echo "    go run wrapper status=$go_run_code; child exit_code=2"

echo "==> exact exit code with built binary"
go build -o "$BIN" ./cmd/pack
set +e
"$BIN" demo --corrupt-boundary --out "${BAD}-code" >/dev/null 2>&1
binary_code=$?
set -e
[[ "$binary_code" == "2" ]] || { echo "binary exit=$binary_code want 2"; exit 1; }
echo "    binary exit code 2 verified"

echo "==> resume"
go run ./cmd/pack demo --resume --out "$BAD" > "$BAD.resume.json"
python3 - "$BAD.resume.json" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
assert r["ok"] is True, r.get("failure")
assert r["alignment_error_ms"] <= r["frame_duration_ms"], r
assert len(r["segment_sha256"]) == 64, r["segment_sha256"]
print(f"    recovered; alignment_error_ms={r['alignment_error_ms']}")
PY

echo "ACCEPTANCE OK"
