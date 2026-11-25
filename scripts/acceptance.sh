#!/usr/bin/env bash
# Local acceptance flow for media-check. No network service is used by the
# program itself; `go mod tidy` may download pinned modules.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

NORMAL_WORK="${NORMAL_WORK:-/tmp/media-check}"
BAD_WORK="${BAD_WORK:-/tmp/media-bad}"
RECOVERY_WORK="${RECOVERY_WORK:-/tmp/media-check-recovery}"

echo "==> go mod tidy"
go mod tidy
echo "==> go build ./..."
go build ./...
echo "==> go test ./..."
go test ./...

echo "==> normal demo: $NORMAL_WORK"
rm -rf "$NORMAL_WORK"
normal_out="$(go run ./cmd/media-check demo --seed 17 --work "$NORMAL_WORK")"
echo "$normal_out"
for expected in '^tracks=3$' '^frames=12$' '^duplicate_groups=1$' \
  '^alignment_error_ms=0\.000$' '^frame_sha256=[0-9a-f]\{64\}$' \
  '^segment_sha256=[0-9a-f]\{64\}$' '^pixel_min=0$' '^pixel_max=255$' \
  '^peak_dbfs=-' '^manifest_sha256=[0-9a-f]\{64\}$'; do
  grep -q "$expected" <<<"$normal_out" || { echo "missing output: $expected" >&2; exit 1; }
done
clean_manifest="$(awk -F= '/^manifest_sha256=/{print $2}' <<<"$normal_out")"

echo "==> corruption demos"
# `go run` wraps a child exit code 2 as its own exit code 1 while printing
# "exit status 2". Build a binary to verify the kernel's exact exit contract.
bin_dir="$(mktemp -d)"
trap 'rm -rf "$bin_dir"' EXIT
go build -o "$bin_dir/media-check" ./cmd/media-check

for kind in truncated-box missing-keyframe timestamp-regression; do
  work="/tmp/media-${kind}"
  rm -rf "$work"
  set +e
  bad_out="$("$bin_dir/media-check" demo --seed 17 --corrupt "$kind" --work "$work" 2>&1)"
  code=$?
  set -e
  echo "$bad_out"
  test "$code" -eq 2
  grep -q '^damage_count=[1-9][0-9]*$' <<<"$bad_out"
  grep -q '^first_bad_offset=[1-9][0-9]*$' <<<"$bad_out"
  grep -q '^track_id=[1-9][0-9]*$' <<<"$bad_out"
  grep -q '^time_range=\[[0-9][0-9]*,[0-9][0-9]*\)ms$' <<<"$bad_out"
done

# Keep the exact requested truncated-box path current as well.
rm -rf "$BAD_WORK"
"$bin_dir/media-check" demo --seed 17 --corrupt truncated-box --work "$BAD_WORK" >/dev/null

echo "==> interrupt/resume: $RECOVERY_WORK"
rm -rf "$RECOVERY_WORK"
go run ./cmd/media-check demo --seed 17 --work "$RECOVERY_WORK" --interrupt-after scan >/dev/null
go run ./cmd/media-check demo --seed 17 --work "$RECOVERY_WORK" --interrupt-after scan >/dev/null
job_count="$(find "$RECOVERY_WORK/jobs" -maxdepth 1 -name '*.json' | wc -l | tr -d ' ')"
test "$job_count" -eq 1
resume_out="$(go run ./cmd/media-check demo --seed 17 --work "$RECOVERY_WORK" --resume)"
echo "$resume_out"
grep -q '^resume_from=scan$' <<<"$resume_out"
resume_manifest="$(awk -F= '/^manifest_sha256=/{print $2}' <<<"$resume_out")"
test "$resume_manifest" = "$clean_manifest"

echo "==> darwin arm64/amd64 cross-build"
GOOS=darwin GOARCH=arm64 go build -o "$bin_dir/media-check-arm64" ./cmd/media-check
GOOS=darwin GOARCH=amd64 go build -o "$bin_dir/media-check-amd64" ./cmd/media-check

echo "ACCEPTANCE_OK manifest_sha256=$clean_manifest"
