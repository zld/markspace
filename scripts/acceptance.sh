#!/usr/bin/env bash
# Acceptance driver for mediascan.
#
# Runs the documented commands, then asserts:
#   * required terminal tokens are present
#   * binary exit codes are 0 (clean) / 2 (fault located) / 1 (usage)
#   * truncation offset matches the generated ground truth exactly
#   * verify SHA-256 is stable across repeated runs
#   * reported memory_peak_bytes <= 8 MiB
#   * resumed elapsed_ms <= 80% of cold elapsed_ms
#
# Note: `go run` wraps a child exit code 2 as the go tool's own exit status 1
# and prints "exit status 2"; the exact program code is therefore checked on a
# compiled binary, while the literal `go run` commands are exercised too.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
FIX="${1:-/tmp/media-fixture}"
BIN="${TMPDIR:-/tmp}/mediascan-acceptance-bin"
MAX_MEM=$((8 * 1024 * 1024))

say() { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }
fail() { echo "ASSERT FAIL: $*" >&2; exit 1; }

say "go mod tidy"
go mod tidy || fail "go mod tidy"
say "go build ./..."
go build ./... || fail "go build"
say "go test ./..."
go test ./... || fail "go test"

say "build assertion binary"
go build -o "$BIN" ./cmd/mediascan

say "demo fixtures -> $FIX"
go run ./cmd/mediascan demo --out "$FIX"

need_tokens() {
  local file="$1"
  for tok in fault_offset_bytes fault_time_ticks gop_min_ticks gop_max_ticks \
             resume_from_sample elapsed_ms memory_peak_bytes sha256; do
    grep -q "$tok=" "$file" || fail "missing token $tok in $file"
  done
}

say "good scan (documented go run)"
go run ./cmd/mediascan scan --input "$FIX/good.m3u8" \
  --checkpoint "$FIX/state" --memory-mib 8 | tee "$FIX/out-good.txt"
need_tokens "$FIX/out-good.txt"

say "corrupt cold scan (documented go run; go wrapper exits 1, program reports exit_code=2)"
set +e
go run ./cmd/mediascan scan --input "$FIX/corrupt.m3u8" \
  --checkpoint "$FIX/recover" --memory-mib 8 > "$FIX/out-corrupt-cold.txt" 2>&1
GO_RUN_RC=$?
set -e
grep -q "exit_code=2" "$FIX/out-corrupt-cold.txt" || fail "corrupt scan did not report exit_code=2"
grep -q "exit status 2" "$FIX/out-corrupt-cold.txt" || fail "go run did not surface exit status 2"
echo "go-run wrapper rc=$GO_RUN_RC (expected 1); program exit_code=2 confirmed"
need_tokens "$FIX/out-corrupt-cold.txt"

say "timed resume (documented /usr/bin/time -p go run --resume)"
/usr/bin/time -p go run ./cmd/mediascan scan --input "$FIX/corrupt.m3u8" \
  --checkpoint "$FIX/recover" --memory-mib 8 --resume 2>&1 | tee "$FIX/out-resume.txt"
need_tokens "$FIX/out-resume.txt"
grep -q "resume_from_sample=2048" "$FIX/out-resume.txt" || fail "resume_from_sample != 2048"

say "verify good twice"
go run ./cmd/mediascan verify --input "$FIX/good.m3u8" | tee "$FIX/out-verify1.txt"
go run ./cmd/mediascan verify --input "$FIX/good.m3u8" | tee "$FIX/out-verify2.txt"
SHA1="$(sed -n 's/^sha256=//p' "$FIX/out-verify1.txt" | head -1)"
SHA2="$(sed -n 's/^sha256=//p' "$FIX/out-verify2.txt" | head -1)"
[ -n "$SHA1" ] || fail "empty verify sha"
[ "$SHA1" = "$SHA2" ] || fail "verify sha mismatch: $SHA1 != $SHA2"

say "binary exit codes"
"$BIN" scan --input "$FIX/good.m3u8" --checkpoint "$FIX/ack-state" --memory-mib 8 >/dev/null
[ $? -eq 0 ] || fail "good binary exit != 0"
set +e
"$BIN" scan --input "$FIX/corrupt.m3u8" --checkpoint "$FIX/ack-recover" --memory-mib 8 >/dev/null
RC=$?
set -e
[ "$RC" -eq 2 ] || fail "corrupt binary exit=$RC want 2"
set +e
"$BIN" scan 2>/dev/null; RC=$?; set -e
[ "$RC" -eq 1 ] || fail "usage exit=$RC want 1"
echo "exit codes: good=0 corrupt=2 usage=1 OK"

say "truncation offset vs ground truth"
CUT="$(sed -n 's/.*"cut_offset_bytes": *\([0-9]*\).*/\1/p' "$FIX/ground_truth.json" | head -1)"
OFF="$(sed -n 's/^fault_offset_bytes=//p' "$FIX/out-corrupt-cold.txt" | head -1)"
[ "$OFF" = "$CUT" ] || fail "fault_offset=$OFF != ground truth cut=$CUT"
echo "fault_offset_bytes=$OFF exactly matches planted cut"

say "memory bound (<= 8 MiB reported peak)"
for f in "$FIX/out-good.txt" "$FIX/out-corrupt-cold.txt" "$FIX/out-resume.txt"; do
  M="$(sed -n 's/^memory_peak_bytes=//p' "$f" | head -1)"
  [ -n "$M" ] || fail "no memory_peak_bytes in $f"
  [ "$M" -le "$MAX_MEM" ] || fail "$f memory peak $M > $MAX_MEM"
  echo "$(basename "$f"): memory_peak_bytes=$M <= $MAX_MEM"
done

say "resume timing ratio (<= 80% of cold)"
COLD="$(sed -n 's/^elapsed_ms=//p' "$FIX/out-corrupt-cold.txt" | head -1)"
RES="$(sed -n 's/^elapsed_ms=//p' "$FIX/out-resume.txt" | head -1)"
echo "cold_elapsed_ms=$COLD resume_elapsed_ms=$RES"
if [ "$COLD" -gt 0 ]; then
  # RES/COLD <= 0.8  <=>  RES*100 <= COLD*80
  if [ $((RES * 100)) -gt $((COLD * 80)) ]; then
    fail "resume ${RES}ms > 80% of cold ${COLD}ms"
  fi
  echo "resume ratio OK"
else
  echo "cold elapsed rounded to 0ms; ratio check skipped (resume=${RES}ms)"
fi

say "ALL ACCEPTANCE CHECKS PASSED"
echo "sha256=$SHA1"
