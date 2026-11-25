#!/usr/bin/env bash
# Local acceptance loop for mediapack. All generated artifacts stay under ./output.
set -euo pipefail

if ! command -v go >/dev/null 2>&1; then
  brew install go
fi

go mod tidy
go build ./...
go test ./...

mkdir -p output
NORMAL_JSON="output/acceptance-normal.json"
RESUME_JSON="output/acceptance-resume.json"

go run ./cmd/mediapack demo --seed 17 --bitrates 2 --queue-capacity 8 --report json | tee "$NORMAL_JSON"
go run ./cmd/mediapack demo --seed 17 --corrupt truncate --report json
go run ./cmd/mediapack demo --seed 17 --loss 0,3,4 --reorder 2 --report json
go run ./cmd/mediapack demo --seed 17 --resume --report json | tee "$RESUME_JSON"

python3 - <<'PY'
import json
from pathlib import Path

normal = json.loads(Path("output/acceptance-normal.json").read_text())
resume = json.loads(Path("output/acceptance-resume.json").read_text())
m = normal["metrics"]

assert m["av_sync_error_ms"] <= 20, m["av_sync_error_ms"]
assert m["p95_latency_ms"] >= 0
assert m["peak_queue_occupancy"] <= 8
counts = m["segment_count_per_representation"]
assert len(counts) == 2 and counts[0] == counts[1], counts
assert normal["manifest_sha256"] == resume["manifest_sha256"]
assert resume["resume"]["resumed_segment_count"] == m["segment_count"]
print("acceptance assertions passed")
PY
