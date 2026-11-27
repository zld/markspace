#!/usr/bin/env bash
# Run the acceptance scenario end to end in an isolated working directory.
set -euo pipefail
cd "$(dirname "$0")/.."

go run ./cmd/media-check demo \
  --seed 17 \
  --loss 0,1,8 \
  --reorder 3 \
  --wrap-seq 65534 \
  --crash-after-artifact \
  --rerun \
  --rollback-to v1 \
  --workdir .media-check-work
