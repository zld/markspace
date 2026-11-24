#!/usr/bin/env bash
# acceptance.sh runs the full local acceptance record for mediarun:
#   build, unit tests, fixture generation, process (+resume/determinism),
#   verify, and the three boundary fixtures (stage + offset + exit 2).
#
# Run from anywhere; it cds to the repository root.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

FIX="${FIX_DIR:-./tmp-fixture}"
OUT="${OUT_DIR:-./tmp-out}"
BIN="$(mktemp -d)/mediarun"
trap 'rm -rf "$(dirname "$BIN")"' EXIT

say()  { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

say "go mod tidy"
go mod tidy || fail "go mod tidy"

say "go build ./..."
go build ./... || fail "build"
go build -o "$BIN" ./cmd/mediarun || fail "build cli"

say "go test ./..."
go test ./... || fail "tests"

say "fixture -> $FIX"
rm -rf "$FIX" "$OUT"
"$BIN" fixture --dir "$FIX" || fail "fixture"

say "process (first run)"
OUT1="$("$BIN" process --input "$FIX" --out "$OUT" \
  --target-bitrate 1200000 --resume)" || fail "first process"
echo "$OUT1"
D1="$(printf '%s\n' "$OUT1" | sed -n 's/.*output_digest=\([0-9a-f]*\).*/\1/p')"
[ -n "$D1" ] || fail "no output digest on first run"

say "process (resume / determinism)"
OUT2="$("$BIN" process --input "$FIX" --out "$OUT" \
  --target-bitrate 1200000 --resume)" || fail "second process"
echo "$OUT2"
D2="$(printf '%s\n' "$OUT2" | sed -n 's/.*output_digest=\([0-9a-f]*\).*/\1/p')"
REUSED="$(printf '%s\n' "$OUT2" | sed -n 's/.*reused_stages=\([0-9]*\).*/\1/p')"
[ "$D1" = "$D2" ] || fail "output digest changed across identical runs"
[ "${REUSED:-0}" -gt 0 ] || fail "expected reused stages > 0, got '$REUSED'"

say "verify"
VOUT="$("$BIN" verify --input "$FIX" --out "$OUT")" || fail "verify"
echo "$VOUT"
echo "$VOUT" | grep -q "output_digest=$D1" || fail "verify digest mismatch"

say "threshold spot checks"
DEV="$(printf '%s\n' "$OUT1" | sed -n 's/.*bitrate_deviation_pct=\([0-9.]*\).*/\1/p')"
ALIGN="$(printf '%s\n' "$OUT1" | sed -n 's/.*max_alignment_error_ms=\([0-9]*\).*/\1/p')"
printf '  deviation=%s%% alignment=%sms\n' "$DEV" "$ALIGN"
awk -v d="$DEV" 'BEGIN{ exit !(d+0 <= 5.0) }' || fail "bitrate deviation > 5%"
awk -v a="$ALIGN" 'BEGIN{ exit !(a+0 <= 20) }' || fail "alignment error > 20ms"
echo "$OUT1" | grep -q 'random_access_hits=100/100' \
  || fail "random-access hit rate not 100%"

say "boundary fixtures (each must report stage+offset and exit 2)"
check_boundary() {
  local name="$1" want_stage="$2"
  local bdir="$FIX/corrupt/$name" bout="./tmp-boundary-out/$name"
  rm -rf "$bout"
  local err code
  err="$("$BIN" process --input "$bdir" --out "$bout" \
      --target-bitrate 1200000 --resume 2>&1 >/dev/null)"
  code=$?
  [ "$code" -eq 2 ] || fail "$name: exit=$code want 2 ($err)"
  echo "$err" | grep -q "stage=$want_stage" \
    || fail "$name: missing stage=$want_stage in: $err"
  echo "$err" | grep -Eq 'offset=[0-9]+' \
    || fail "$name: missing numeric offset in: $err"
  printf '  %-10s exit=2 %s\n' "$name" "$err"
}
check_boundary truncated parse-box-header
check_boundary zerots    manifest
check_boundary dupts     repair

say "ALL ACCEPTANCE CHECKS PASSED"
