#!/usr/bin/env bash
# Local acceptance runner for mediacheck. Purely local, no external services.
# Verifies tidy/build/test plus the three demo paths and their exit codes.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "== go mod tidy =="
go mod tidy

echo "== go build ./... =="
go build ./...
BIN="$(mktemp -d)/mediacheck"
go build -o "$BIN" ./cmd/mediacheck
run_demo() { "$BIN" "$@"; }

echo "== go test ./... =="
go test ./...

SEED=17

echo "== demo healthy =="
OUT="$(run_demo demo --seed "$SEED" --faults none)"
echo "$OUT"
HEALTHY_CHECK='import json,sys
d=json.loads(sys.argv[1])
assert abs(d["bitrate_deviation_pct"]) <= 5.0, d["bitrate_deviation_pct"]
assert isinstance(d["quality_mean"], (int,float)) and d["quality_mean"]==d["quality_mean"]
assert isinstance(d["quality_p95"], (int,float)) and d["quality_p95"]==d["quality_p95"]
assert d["manifest_alignment_ms"] <= 20.0
for k in ("target_bitrate_bps","actual_bitrate_bps","damaged_ranges","recovered_transactions","report_sha256"):
    assert k in d, k
print("healthy assertions OK")'
python3 -c "$HEALTHY_CHECK" "$OUT"

echo "== demo damaged (expect exit code 2) =="
set +e
OUT="$(run_demo demo --seed "$SEED" --faults truncate,late,metadata)"
RC=$?
set -e
echo "$OUT"
[ "$RC" -eq 2 ] || { echo "expected exit 2, got $RC"; exit 1; }
DAMAGED_CHECK='import json,sys
d=json.loads(sys.argv[1])
assert len(d["damaged_ranges"]) >= 2, d["damaged_ranges"]
fields={c["field"] for c in d["metadata_conflicts"]}
assert {"channel_layout","sample_rate","color_metadata"} <= fields, fields
print("damaged assertions OK")'
python3 -c "$DAMAGED_CHECK" "$OUT"

echo "== crash then resume (hash must equal clean rerun) =="
rm -rf "${TMPDIR:-/tmp}/seed-$SEED"
set +e
run_demo demo --seed "$SEED" --faults truncate,late,metadata \
  --crash-after validate
CRASH_RC=$?
set -e
[ "$CRASH_RC" -eq 1 ] || { echo "expected crash exit 1, got $CRASH_RC"; exit 1; }
# Resume and clean runs both report damage (exit 2) by design; tolerate that
# code while capturing their JSON for the hash comparison.
set +e
RESUME="$(run_demo demo --seed "$SEED" --faults truncate,late,metadata \
  --crash-after validate --resume)"
RESUME_RC=$?
CLEAN="$(run_demo demo --seed "$SEED" --faults truncate,late,metadata)"
CLEAN_RC=$?
set -e
[ "$RESUME_RC" -eq 2 ] || { echo "expected resume damaged exit 2, got $RESUME_RC"; exit 1; }
[ "$CLEAN_RC" -eq 2 ] || { echo "expected clean damaged exit 2, got $CLEAN_RC"; exit 1; }
H1="$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["report_sha256"])' "$RESUME")"
H2="$(python3 -c 'import json,sys;print(json.loads(sys.argv[1])["report_sha256"])' "$CLEAN")"
[ "$H1" = "$H2" ] || { echo "hash mismatch: $H1 != $H2"; exit 1; }
echo "resume hash == clean rerun hash: $H1"
python3 -c 'import json,sys;d=json.loads(sys.argv[1]);assert d["recovered_transactions"]>=1;print("recovery assertions OK")' "$RESUME"

echo "ALL ACCEPTANCE CHECKS PASSED"
