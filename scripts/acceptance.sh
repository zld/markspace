#!/usr/bin/env bash
# acceptance.sh runs the exact acceptance workflow from the specification and
# checks every stated criterion. macOS Terminal, no external services.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

SAMPLES="./tmp/samples"
WORK="./tmp/acceptance-work"
rm -rf "$SAMPLES" "$WORK"
mkdir -p "$WORK"

fail=0
say() { printf '%s\n' "$*"; }
check() {
  # check <description> <condition-result>
  local rc="${2:-1}"
  if [[ "$rc" == "0" ]]; then
    say "  PASS  $1"
  else
    say "  FAIL  $1"
    fail=1
  fi
}

BIN="./tmp/acceptance-bin"
say "== build =="
go build ./... || { say "build failed"; exit 3; }
go build -o "$BIN" ./cmd/verify || { say "verify build failed"; exit 3; }
go test ./... >/dev/null || { say "unit tests failed"; exit 3; }
say "  PASS  build and unit tests"

say "== fixtures =="
"$BIN" --generate "$SAMPLES" || { say "generation failed"; exit 3; }
check "generated good and corrupt samples" $?

json_get() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)"; }

say "== good input =="
set +e
GOOD_OUT="$("$BIN" --input "$SAMPLES/good" --segment 2s --bitrates 500k,1000k --report json --work "$WORK/good")"
good_exit=$?
set -e
check "good exit code is 0" $([[ $good_exit -eq 0 ]] && echo 0 || echo 1)

read -r seg_count manifest av_sync rate p95 < <(echo "$GOOD_OUT" | python3 -c '
import sys,json
d=json.load(sys.stdin)
print(d["segment_count"], d["manifest_sha256"], d["av_sync_error_ms"], d["packet_recovery_rate"], d["p95_latency_ms"])
')
say "  segment_count=$seg_count manifest_sha256=$manifest av_sync_error_ms=$av_sync packet_recovery_rate=$rate p95_latency_ms=$p95"
check "segment_count > 0" $([[ "$seg_count" -gt 0 ]] && echo 0 || echo 1)
check "manifest_sha256 present" $([[ -n "$manifest" && "$manifest" != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" ]] && echo 0 || echo 1)
check "av_sync_error_ms <= 40" $([[ "$av_sync" -le 40 ]] && echo 0 || echo 1)
check "packet_recovery_rate is 1.0" $([[ "$rate" == "1.0" || "$rate" == "1" ]] && echo 0 || echo 1)
if python3 -c "import math,sys;v=float('$p95');sys.exit(0 if math.isfinite(v) and v>=0 else 1)"; then
  check "p95_latency_ms finite and non-negative" 0
else
  check "p95_latency_ms finite and non-negative" 1
fi

# Boundary spread must not exceed one video frame (33.333ms at 30fps).
SPREAD=$(echo "$GOOD_OUT" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(max(f.get("max_layer_boundary_spread_us",0) for f in d["files"]))')
say "  max layer boundary spread = ${SPREAD}us"
check "layer boundary spread <= one video frame (33334us)" $([[ "$SPREAD" -le 33334 ]] && echo 0 || echo 1)

say "== corrupt input =="
set +e
CORRUPT_OUT="$("$BIN" --input "$SAMPLES/corrupt" --report json --work "$WORK/corrupt")"
corrupt_exit=$?
set -e
check "corrupt exit code is 2" $([[ $corrupt_exit -eq 2 ]] && echo 0 || echo 1)
set +e
echo "$CORRUPT_OUT" | python3 -c '
import sys,json
d=json.load(sys.stdin)
for f in d["files"]:
    e=f.get("error") or {}
    print("  file:", f["path"].split("/")[-1], "category:", e.get("kind","-"), "ok:", f["ok"])
print("  failure_count:", d["failure_count"])
sys.exit(0 if d["failure_count"] > 0 else 1)
'
corrupt_check=$?
set -e
check "corrupt report lists files with categories and non-zero failures" $corrupt_check

say "== interrupt and resume =="
set +e
"$BIN" --input "$SAMPLES/good" --segment 2s --bitrates 500k,1000k --interrupt-after 1 --work "$WORK/resume" >/dev/null 2>&1
int_exit=$?
set -e
check "interrupt exit code is 10" $([[ $int_exit -eq 10 ]] && echo 0 || echo 1)

set +e
RESUME_OUT="$("$BIN" --input "$SAMPLES/good" --segment 2s --bitrates 500k,1000k --resume --report json --work "$WORK/resume")"
resume_exit=$?
set -e
check "resume exit code is 0" $([[ $resume_exit -eq 0 ]] && echo 0 || echo 1)
resumed_segs=$(echo "$RESUME_OUT" | python3 -c 'import sys,json;print(json.load(sys.stdin)["segment_count"])')
resumed_manifest=$(echo "$RESUME_OUT" | python3 -c 'import sys,json;print(json.load(sys.stdin)["manifest_sha256"])')
say "  resumed segment_count=$resumed_segs manifest_sha256=$resumed_manifest"

# Compare against a clean, uninterrupted run in a fresh work directory.
set +e
FULL_OUT="$("$BIN" --input "$SAMPLES/good" --segment 2s --bitrates 500k,1000k --report json --work "$WORK/full")"
set -e
full_segs=$(echo "$FULL_OUT" | python3 -c 'import sys,json;print(json.load(sys.stdin)["segment_count"])')
full_manifest=$(echo "$FULL_OUT" | python3 -c 'import sys,json;print(json.load(sys.stdin)["manifest_sha256"])')
say "  uninterrupted segment_count=$full_segs manifest_sha256=$full_manifest"
check "resumed segment count equals uninterrupted" $([[ "$resumed_segs" -eq "$full_segs" ]] && echo 0 || echo 1)
check "resumed manifest digest equals uninterrupted" $([[ "$resumed_manifest" == "$full_manifest" ]] && echo 0 || echo 1)

# No duplicate segment entries across layers/numbers.
dup_check=$(echo "$RESUME_OUT" | python3 -c '
import sys,json,glob,os
d=json.load(sys.stdin)
dups=0
for f in d["files"]:
    sha=f["sha256"][:16]
    p=os.path.join(sys.argv[1], sha, "manifest.json")
    if os.path.exists(p):
        m=json.load(open(p))
        keys=[(e["number"],e["layer"]) for e in m["entries"]]
        dups += len(keys)-len(set(keys))
print(dups)
sys.exit(0 if dups==0 else 1)
' "$WORK/resume")
check "no duplicate manifest entries after resume" $?

say "== input mutation invalidates index and checkpoint =="
GOOD_FILE="$SAMPLES/good/baseline.mp4"
BEFORE_INDEX=$(echo "$FULL_OUT" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(next(f["index_digest"] for f in d["files"] if f["path"].endswith("baseline.mp4")))')
python3 - "$GOOD_FILE" <<'PY'
import sys
p=sys.argv[1]
b=bytearray(open(p,"rb").read())
for i in range(len(b)-120, len(b)-80):
    b[i] ^= 0xFF
open(p,"wb").write(bytes(b))
PY
set +e
AFTER_OUT="$("$BIN" --input "$SAMPLES/good" --segment 2s --bitrates 500k,1000k --resume --report json --work "$WORK/resume")"
mut_exit=$?
set -e
check "mutated input run still exits 0" $([[ $mut_exit -eq 0 ]] && echo 0 || echo 1)
AFTER_INDEX=$(echo "$AFTER_OUT" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(next(f["index_digest"] for f in d["files"] if f["path"].endswith("baseline.mp4")))')
AFTER_MANIFEST=$(echo "$AFTER_OUT" | python3 -c 'import sys,json;print(json.load(sys.stdin)["manifest_sha256"])')
say "  index before=$BEFORE_INDEX"
say "  index after =$AFTER_INDEX"
check "index digest changed after input mutation" $([[ "$BEFORE_INDEX" != "$AFTER_INDEX" ]] && echo 0 || echo 1)
check "manifest changed after input mutation" $([[ "$full_manifest" != "$AFTER_MANIFEST" ]] && echo 0 || echo 1)

say ""
if [[ $fail -eq 0 ]]; then
  say "ACCEPTANCE PASSED"
else
  say "ACCEPTANCE FAILED"
  exit 2
fi
