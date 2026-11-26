#!/usr/bin/env bash
# End-to-end acceptance checks for replayctl.
# Status-sensitive failure/recovery cases use a built binary because the go
# tool wraps a non-zero child status and exits 1 from `go run`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
GO="${GO:-go}"
WORK="$ROOT/.tmp/acceptance"
rm -rf "$WORK"
mkdir -p "$WORK/bin"

fail() { echo "ACCEPTANCE FAIL: $*" >&2; exit 1; }
assert_contains() { grep -Fq "$2" "$1" || fail "$3 (missing: $2)"; }
field() { sed -n "s/^$2=//p" "$1" | head -n1; }

echo "== go mod tidy / build / test =="
"$GO" mod tidy
"$GO" build ./...
"$GO" test ./...
"$GO" build -o "$WORK/bin/replayctl" ./cmd/replayctl
BIN="$WORK/bin/replayctl"

echo "== normal sample =="
rm -rf "$ROOT/.tmp/ok"
# These are the exact terminal invocations from the acceptance record.
"$GO" run ./cmd/replayctl demo --self-fixture --event-ms 1000 --pre-ms 500 --post-ms 500 --out .tmp/ok \
  | tee "$WORK/ok1.out"
"$GO" run ./cmd/replayctl verify --dir .tmp/ok | tee "$WORK/verify.out"

for want in \
  "frames=25" "pixels=6400" "audio_samples=48000" "peak_dbfs=-12.04" \
  "video_align_error_ms=20" "audio_boundary_error_samples=0"; do
  assert_contains "$WORK/ok1.out" "$want" "normal metrics"
done
HASH1="$(field "$WORK/ok1.out" sha256)"
[[ "$HASH1" =~ ^[0-9a-f]{64}$ ]] || fail "sha256 is not 64 lowercase hex: $HASH1"

"$GO" run ./cmd/replayctl demo --self-fixture --event-ms 1000 --pre-ms 500 --post-ms 500 --out .tmp/ok \
  >"$WORK/ok2.out"
HASH2="$(field "$WORK/ok2.out" sha256)"
[[ "$HASH1" == "$HASH2" ]] || fail "repeated normal hash changed: $HASH1 != $HASH2"

echo "== truncated box =="
set +e
"$BIN" demo --self-fixture --case truncated-box --out "$WORK/truncated" \
  >"$WORK/truncated.out" 2>&1
CODE=$?
set -e
[[ "$CODE" -eq 2 ]] || fail "truncated exit=$CODE want2"
assert_contains "$WORK/truncated.out" "TRUNCATED_BOX offset=16" "truncated box"
[[ ! -e "$WORK/truncated" ]] || fail "truncated case created delivery state"

echo "== empty clip segment =="
set +e
"$BIN" demo --self-fixture --case time-gap --event-ms 1000 --pre-ms 500 --post-ms 500 \
  --out "$WORK/gap" >"$WORK/gap.out" 2>&1
CODE=$?
set -e
[[ "$CODE" -eq 2 ]] || fail "time-gap exit=$CODE want2"
assert_contains "$WORK/gap.out" "TIME_GAP" "time gap code"
grep -Eq 'offset=[0-9]+' "$WORK/gap.out" || fail "time gap missing numeric offset"
grep -Eq 'raw_offset=[0-9]+' "$WORK/gap.out" || fail "time gap missing raw offset"
[[ ! -e "$WORK/gap" ]] || fail "time-gap case created delivery state"

echo "== timestamp wrap fixture =="
"$BIN" demo --self-fixture --case timestamp-wrap --event-ms 1000 --pre-ms 500 --post-ms 500 \
  --out "$WORK/wrap" >"$WORK/wrap.out" 2>&1
assert_contains "$WORK/wrap.out" "frames=25" "wrap metrics"
assert_contains "$WORK/wrap.out" "sha256=" "wrap hash"

echo "== rename-after crash recovery =="
REC="$WORK/recover"
RECOVER_ARGS=(demo --self-fixture --case recover --event-ms 1000 --pre-ms 500 --post-ms 500 --out "$REC")
set +e
"$BIN" "${RECOVER_ARGS[@]}" >"$WORK/rec1.out" 2>&1
CODE=$?
set -e
[[ "$CODE" -eq 75 ]] || fail "first recovery exit=$CODE want75"
grep -q "INJECTED_EXIT" "$WORK/rec1.out" || fail "missing injected exit marker"
MP4_COUNT="$(find "$REC/deliveries" -maxdepth 1 -name '*.mp4' 2>/dev/null | wc -l | tr -d ' ')"
[[ "$MP4_COUNT" -eq 1 ]] || fail "expected one renamed mp4 after crash, got $MP4_COUNT"
[[ ! -e "$REC/journal.jsonl" ]] || fail "journal committed before injected crash"

"$BIN" "${RECOVER_ARGS[@]}" >"$WORK/rec2.out" 2>&1
assert_contains "$WORK/rec2.out" "recovered=1 published=1 duplicate=0" "second recovery counters"
RHASH1="$(field "$WORK/rec2.out" sha256)"
[[ "$RHASH1" =~ ^[0-9a-f]{64}$ ]] || fail "recovery hash invalid"

"$BIN" "${RECOVER_ARGS[@]}" >"$WORK/rec3.out" 2>&1
assert_contains "$WORK/rec3.out" "recovered=0 published=1 duplicate=0" "third recovery counters"
RHASH2="$(field "$WORK/rec3.out" sha256)"
[[ "$RHASH1" == "$RHASH2" ]] || fail "recovery hash changed after rerun"
JOURNAL_LINES="$(wc -l < "$REC/journal.jsonl" | tr -d ' ')"
[[ "$JOURNAL_LINES" -eq 1 ]] || fail "journal lines=$JOURNAL_LINES want1"
"$BIN" verify --dir "$REC" >"$WORK/recover-verify.out"

echo "acceptance: PASS"
echo "normal sha256=$HASH1"
echo "recovery sha256=$RHASH1"
