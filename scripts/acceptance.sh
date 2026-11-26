#!/usr/bin/env bash
set -euo pipefail

# End-to-end acceptance checks for mediasync.
# Run from the repository root: scripts/acceptance.sh

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

BIN="/tmp/media-sync"
FIX=".tmp/fixtures"
OUT=".tmp/out"
REPEAT=".tmp/repeat"
CHANGED=".tmp/out-changed"
CANCEL=".tmp/cancel"

say() { printf '\n== %s ==\n' "$*"; }
fail() { echo "ACCEPTANCE-FAIL: $*" >&2; exit 1; }

expect_rc() {
  local want="$1" got="$2" label="$3"
  if [[ "$got" != "$want" ]]; then
    fail "$label: exit code $got, want $want"
  fi
}

run_capture() {
  local file="$1"; shift
  local rc=0
  "$@" >"$file" 2>&1 || rc=$?
  echo "$rc"
}

say "tidy, build, test, generate"
go mod tidy
go build -o "$BIN" ./cmd/mediasync
go test ./...
rm -rf .tmp
go run ./cmd/fixturegen --dir "$FIX" --seed 7

say "valid run"
rc=0
"$BIN" run --input "$FIX/valid" --out "$OUT" \
  --budget-units 100000 --max-bytes 1048576 | tee .tmp/run.txt || rc=${PIPESTATUS[0]}
expect_rc 0 "$rc" "valid run"
for token in 'tracks=3' 'unique=9' 'duplicates=1' 'gaps=1' 'loss=10.00%' 'frames=4'; do
  grep -Fq "$token" .tmp/run.txt || fail "missing $token"
done
grep -Eq 'root_hash=[0-9a-f]{64}$' .tmp/run.txt || fail "missing 64-char root hash"

say "verify"
"$BIN" verify --out "$OUT" | tee .tmp/verify.txt

say "repeat and compare"
rm -rf "$REPEAT"
"$BIN" run --input "$FIX/valid" --out "$REPEAT" \
  --budget-units 100000 --max-bytes 1048576 >/dev/null
"$BIN" compare --a "$OUT" --b "$REPEAT" | tee .tmp/compare-repeat.txt
grep -Fq 'equal=true' .tmp/compare-repeat.txt || fail "repeat hash mismatch"

say "changed input compares false"
rm -rf "$CHANGED"
"$BIN" run --input "$FIX/changed" --out "$CHANGED" \
  --budget-units 100000 --max-bytes 1048576 >/dev/null
"$BIN" compare "$OUT" "$CHANGED" | tee .tmp/compare-changed.txt
grep -Fq 'equal=false' .tmp/compare-changed.txt || fail "changed input did not change hash"

say "corrupt RTP exits 2 with phase and offset"
rc=$(run_capture .tmp/rtp-error.txt "$BIN" run --input "$FIX/corrupt-rtp" --out .tmp/bad-rtp)
cat .tmp/rtp-error.txt
expect_rc 2 "$rc" "corrupt RTP"
grep -Fq 'phase=ingest.rtp' .tmp/rtp-error.txt || fail "RTP phase missing"
grep -Eq 'byte_offset=[0-9]+' .tmp/rtp-error.txt || fail "RTP byte offset missing"
test ! -e .tmp/bad-rtp/manifest.json || fail "RTP failure wrote committed catalog"

say "truncated MP4 exits 2 with phase and offset"
rc=$(run_capture .tmp/mp4-error.txt "$BIN" run --input "$FIX/corrupt-mp4" --out .tmp/bad-mp4)
cat .tmp/mp4-error.txt
expect_rc 2 "$rc" "truncated MP4"
grep -Fq 'phase=ingest.mp4' .tmp/mp4-error.txt || fail "MP4 phase missing"
grep -Eq 'byte_offset=[0-9]+' .tmp/mp4-error.txt || fail "MP4 byte offset missing"
test ! -e .tmp/bad-mp4/manifest.json || fail "MP4 failure wrote committed catalog"

say "timestamp overflow exits 2 without committed catalog"
rc=$(run_capture .tmp/overflow.txt "$BIN" run --input "$FIX/overflow" --out .tmp/overflow-out)
cat .tmp/overflow.txt
expect_rc 2 "$rc" "overflow"
grep -Fq 'timestamp overflow' .tmp/overflow.txt || fail "overflow marker missing"
test ! -e .tmp/overflow-out/manifest.json || fail "overflow wrote committed catalog"

say "cancel after four units exits 130 and resumes"
rm -rf "$CANCEL"
rc=$(run_capture .tmp/cancel.txt "$BIN" run --input "$FIX/valid" --out "$CANCEL" --cancel-after-units 4)
cat .tmp/cancel.txt
expect_rc 130 "$rc" "cancel"
grep -Fq 'state=paused' .tmp/cancel.txt || fail "paused state missing"
grep -Fq 'resume_track="opus-main"' .tmp/cancel.txt || fail "resume track missing"
grep -Eq 'resume_record=3|next_record=4' .tmp/cancel.txt || fail "resume position missing"

"$BIN" run --input "$FIX/valid" --out "$CANCEL" --resume \
  --budget-units 100000 --max-bytes 1048576 | tee .tmp/resume.txt
"$BIN" compare --a "$OUT" --b "$CANCEL" | tee .tmp/compare-resume.txt
grep -Fq 'equal=true' .tmp/compare-resume.txt || fail "resume hash mismatch"

say "quality and budget exit codes"
rc=$(run_capture .tmp/quality.txt "$BIN" run --input "$FIX/quality-drift" --out .tmp/quality)
expect_rc 3 "$rc" "quality gate"
grep -Fq 'phase=quality_gate' .tmp/quality.txt || fail "quality phase missing"

rc=$(run_capture .tmp/budget.txt "$BIN" run --input "$FIX/valid" --out .tmp/budget --budget-units 3)
expect_rc 4 "$rc" "budget limit"
grep -Fq 'state=paused' .tmp/budget.txt || fail "budget pause missing"

say "all acceptance checks passed"
