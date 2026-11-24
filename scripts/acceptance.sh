#!/usr/bin/env bash
# Reproduce the medpipe acceptance run on macOS. The demo intentionally exits
# with the input-corruption code because it also generates a corrupt sample.
set -euo pipefail

cd "$(dirname "$0")/.."

if command -v brew >/dev/null 2>&1; then
  brew install go@1.23
fi
if [ -x /opt/homebrew/opt/go@1.23/bin/go ]; then
  export PATH="/opt/homebrew/opt/go@1.23/bin:$PATH"
fi

go mod tidy
go build ./...
go test ./...

# Show the exact requested invocation. Under `go run`, a child exit code 2 is
# wrapped by the go tool as exit status 1; the JSON exit_code remains 2.
set +e
go run ./cmd/medpipe demo --seed 17 \
  --faults duplicate,reorder,burst-loss,cc-jump,psi-version,truncate \
  --resume
go_run_rc=$?
set -e
echo "go run wrapper exit code: ${go_run_rc} (expected go tool wrapper status 1 for reported input corruption)"

# A built binary exposes the stable product exit code directly.
bin="${TMPDIR:-/tmp}/medpipe-acceptance"
go build -o "$bin" ./cmd/medpipe
json="${TMPDIR:-/tmp}/medpipe-acceptance.json"
set +e
"$bin" demo --seed 17 \
  --faults duplicate,reorder,burst-loss,cc-jump,psi-version,truncate \
  --resume > "$json"
bin_rc=$?
set -e

if [ "$bin_rc" -ne 2 ]; then
  echo "expected binary exit code 2, got $bin_rc" >&2
  exit 1
fi

python3 - "$json" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))
assert r["sync_error_ms"] <= 20, r["sync_error_ms"]
assert r["loss_recovery_rate"] >= 0.95, r["loss_recovery_rate"]
assert r["latency_samples"] > 0 and r["p95_latency_ms"] >= 0
assert r["dropped_duplicates"] > 0
assert r["gap_events"] > 0
assert r["psi_version"] >= 0
assert len(r["output_sha256"]) == 64
assert r["rollback_count"] >= 1
assert r["exit_code"] == 2
assert r["resume"]["pre_resume_sha256"] == r["output_sha256"]
print("acceptance JSON checks passed")
PY

echo "acceptance complete; report: $json"
