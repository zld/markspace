#!/usr/bin/env bash
# Local acceptance check for mediapipeline. No external services are used.
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

go mod download
BIN="${TMPDIR:-/tmp}/mediapipeline-verify-$$"
trap 'rm -f "$BIN"' EXIT
go build -o "$BIN" ./cmd/mediapipeline
go test -v ./...

DEMO_DIR="$(mktemp -d)"
echo "DEMO_DIR=$DEMO_DIR"
set +e
"$BIN" demo --work "$DEMO_DIR" --workers 2 --queue 2 --chunk-bytes 1048576 | tee "$DEMO_DIR/output.log"
normal_code=${PIPESTATUS[0]}
set -e
if [[ "$normal_code" -ne 0 && "$normal_code" -ne 2 ]]; then
  echo "unexpected normal demo exit: $normal_code" >&2
  exit 1
fi
for needle in 'roundtrip=100.00%' 'keyframe_hit=100.00%' 'segment_sha256=100.00%' 'peak_chunks=4' \
  'class=truncated_moov' 'class=stco_out_of_bounds' 'class=backward_timestamp' \
  'class=container_only_change' 'sample_recompute=0'; do
  grep -Fq "$needle" "$DEMO_DIR/output.log" || { echo "missing: $needle" >&2; exit 1; }
done

RECOVERY_DIR="$(mktemp -d)"
echo "RECOVERY_DIR=$RECOVERY_DIR"
set +e
"$BIN" demo --work "$RECOVERY_DIR" --workers 2 --queue 2 --chunk-bytes 1048576 --crash-after-publish 1
crash_code=$?
set -e
printf 'crash_exit=%s\n' "$crash_code"
[[ "$crash_code" -eq 86 ]]

set +e
"$BIN" demo --work "$RECOVERY_DIR" --workers 2 --queue 2 --chunk-bytes 1048576 | tee "$RECOVERY_DIR/rerun.log"
rerun_code=${PIPESTATUS[0]}
set -e
if [[ "$rerun_code" -ne 0 && "$rerun_code" -ne 2 ]]; then
  echo "unexpected recovery rerun exit: $rerun_code" >&2
  exit 1
fi
grep -Fq 'recovered=1 republished=0 missing=0 corrupt=0 duplicates=0' "$RECOVERY_DIR/rerun.log"

echo 'acceptance checks passed'
