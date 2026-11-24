#!/usr/bin/env bash
# End-to-end verification for archivecheck. Purely local, no network use.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

BW="800,420,1100"
pass=0
fail=0

check() { # desc expected actual
  if [ "$2" = "$3" ]; then
    echo "  PASS: $1 (= $3)"
    pass=$((pass + 1))
  else
    echo "  FAIL: $1 (want $2, got $3)"
    fail=$((fail + 1))
  fi
}

echo "== build / vet / test =="
go build ./... || exit 1
go vet ./... || exit 1
go test ./... || exit 1

go build -o "$WORK/archivecheck" ./cmd/archivecheck
BIN="$WORK/archivecheck"

echo "== valid =="
"$BIN" --fixture valid --tracks 3 --bandwidth-kbps "$BW" --window 4 \
  --out "$WORK/frames" > "$WORK/valid.json"
check "valid exit code" 0 "$?"

echo "== burst-loss =="
"$BIN" --fixture burst-loss --tracks 3 --window 2 \
  --out "$WORK/bad" > "$WORK/bad.json"
check "burst-loss exit code" 1 "$?"

echo "== repaired resume =="
"$BIN" --fixture repaired --tracks 3 --window 4 \
  --resume "$WORK/bad" --out "$WORK/recovered" \
  > "$WORK/rec.json"
check "repaired exit code" 0 "$?"

echo "== truncated =="
"$BIN" --fixture truncated --tracks 3 --bandwidth-kbps "$BW" --window 4 \
  --out "$WORK/trunc" > "$WORK/trunc.json"
check "truncated exit code" 0 "$?"

echo "== duplicate-order =="
"$BIN" --fixture duplicate-order --tracks 3 --bandwidth-kbps "$BW" \
  --window 4 --out "$WORK/dup" > "$WORK/dup.json"
check "duplicate-order exit code" 0 "$?"

echo "== bad arguments =="
"$BIN" --fixture nope --tracks 1 --bandwidth-kbps 100 --window 4 \
  --out "$WORK/x" >/dev/null 2>&1
check "bad fixture exit code" 2 "$?"

echo "== semantic assertions (python3) =="
python3 - "$WORK" <<'PY'
import json, os, sys
w = sys.argv[1]
def load(p): return json.load(open(os.path.join(w, p)))
valid = load("valid.json")
bad   = load("bad.json")
rec   = load("rec.json")
trunc = load("trunc.json")
dup   = load("dup.json")

assert valid["status"] == "completed"
assert valid["frame_count"] == 270
assert valid["manifest_alignment_ms"] <= 1.0
assert len(valid["frame_hashes"]) == 270
assert valid["lost_packets"] == 0 and valid["corrupt_frames"] == 0
for k in ("target_bitrate_kbps", "actual_bitrate_kbps", "deviation_pct",
          "decoded_frames", "mean_luma", "sample_count"):
    assert k in valid, k

assert bad["status"] == "blocked" and bad["lost_packets"] > 0
assert 1 in bad["affected_tracks"]
assert bad["blocking_dependency"]["subsystem"] == "transport"

assert rec["status"] == "completed" and rec["recovered_nodes"] > 0
assert rec["executed_nodes"] < len(rec["node_states"])

def hashes(d):
    m = json.load(open(os.path.join(d, "frames.json")))
    return [f["pixel_hash"] for f in m["frames"]]
assert hashes(os.path.join(w, "recovered")) == hashes(os.path.join(w, "frames"))

assert trunc["corrupt_frames"] > 0 and trunc["truncated_segments"] > 0
assert dup["duplicate_packets"] > 0 and dup["corrupt_frames"] == 0
assert hashes(os.path.join(w, "dup")) == hashes(os.path.join(w, "frames"))
print("  PASS: all semantic assertions")
PY
[ $? -eq 0 ] && pass=$((pass + 1)) || fail=$((fail + 1))

echo
echo "RESULT: $pass passed, $fail failed"
[ "$fail" -eq 0 ]
