#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

go mod tidy
go build ./...
go test ./...

rm -rf .medcheck medcheck-output.mp4 medcheck-checkpoint.jsonl

first="$(go run ./cmd/medcheck demo --seed 17 --loss 4,9 --reorder 3 --checkpoint-every 5)"
echo "$first"
for field in packets duplicates reordered recovered av_drift_ms first_bad_offset resume_ms sha256; do
  grep -q "\"$field\"" <<<"$first"
done

first_repeat="$(go run ./cmd/medcheck demo --seed 17 --loss 4,9 --reorder 3 --checkpoint-every 5)"
sha1="$(sed -n 's/.*"sha256": "\([^"]*\)".*/\1/p' <<<"$first")"
sha1_repeat="$(sed -n 's/.*"sha256": "\([^"]*\)".*/\1/p' <<<"$first_repeat")"
[[ "$sha1" == "$sha1_repeat" ]]

resume="$(go run ./cmd/medcheck demo --seed 17 --truncate-at 7 --resume)"
echo "$resume"
sha2="$(sed -n 's/.*"sha256": "\([^"]*\)".*/\1/p' <<<"$resume")"
resume_repeat="$(go run ./cmd/medcheck demo --seed 17 --truncate-at 7 --resume)"
sha2_repeat="$(sed -n 's/.*"sha256": "\([^"]*\)".*/\1/p' <<<"$resume_repeat")"
[[ "$sha2" == "$sha2_repeat" ]]

go run ./cmd/medcheck cases >/tmp/medcheck-cases.json
grep -q AUDIO_CHANNEL_MAPPED /tmp/medcheck-cases.json
grep -q AUDIO_SAMPLE_RATE_CONFLICT /tmp/medcheck-cases.json
grep -q VIDEO_COLOR_DEFAULTED /tmp/medcheck-cases.json
grep -q INVALID_PACKET /tmp/medcheck-cases.json

# Corrupt latest checkpoint record, then recover from the prior boundary.
go run ./cmd/medcheck demo --seed 17 --truncate-at 7 --resume \
  --corrupt-checkpoint --checkpoint .medcheck/corrupt.jsonl \
  --output .medcheck/corrupt.mp4 >/tmp/medcheck-corrupt.json
grep -q CHECKPOINT_INVALID /tmp/medcheck-corrupt.json

# An unresolved video gap must be non-zero.
if go run ./cmd/medcheck demo --seed 17 --unrecoverable \
  --checkpoint .medcheck/unrecoverable.jsonl \
  --output .medcheck/unrecoverable.mp4 >/tmp/medcheck-unrecoverable.json; then
  echo "expected unrecoverable run to fail" >&2
  exit 1
fi

echo "medcheck verification passed"
