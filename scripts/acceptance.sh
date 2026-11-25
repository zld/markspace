#!/usr/bin/env bash
# Local macOS acceptance run. All state stays under ./tmp-fixtures and ./tmp-out.
set -u -o pipefail
cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
  brew install go
fi

go mod tidy
go build ./...
go test ./...

rm -rf ./tmp-fixtures ./tmp-out
go run ./cmd/archivecheck -generate ./tmp-fixtures

BITRATES=400000,900000
VALID=./tmp-fixtures/valid.json

# The checkpoint invocation intentionally stops after one committed stage.
set +e
go run ./cmd/archivecheck -input "$VALID" -bitrates "$BITRATES" \
  -resume-after 1 -out ./tmp-out > ./tmp-out-checkpoint.log 2>&1
checkpoint_code=$?
set -e
if [ "$checkpoint_code" -eq 0 ]; then
  echo "expected -resume-after 1 to checkpoint/stop non-zero" >&2
  exit 1
fi
grep -q "error_kind=simulated_interruption" ./tmp-out-checkpoint.log
grep -q "error_stage=scanned" ./tmp-out-checkpoint.log

# First recovery process: reads the journal and executes uncommitted stages.
go run ./cmd/archivecheck -input "$VALID" -bitrates "$BITRATES" -out ./tmp-out \
  | tee ./tmp-run-1.log
grep -q "journal_recovery=true" ./tmp-run-1.log
grep -q "resume_source=journal" ./tmp-run-1.log

# Second complete process: completed journal is verified and output is stable.
go run ./cmd/archivecheck -input "$VALID" -bitrates "$BITRATES" -out ./tmp-out \
  | tee ./tmp-run-2.log

h1=$(sed -n 's/^manifest_sha256=\([0-9a-f]*\).*/\1/p' ./tmp-run-1.log)
h2=$(sed -n 's/^manifest_sha256=\([0-9a-f]*\).*/\1/p' ./tmp-run-2.log)
a1=$(sed -n 's/^artifact_set_sha256=\([0-9a-f]*\).*/\1/p' ./tmp-run-1.log | head -1)
a2=$(sed -n 's/^artifact_set_sha256=\([0-9a-f]*\).*/\1/p' ./tmp-run-2.log | head -1)
[ "$h1" = "$h2" ]
[ "$a1" = "$a2" ]
grep -q "repeat_manifest_byte_diff=0" ./tmp-run-2.log
grep -q "repeat_artifact_digest_diff=0" ./tmp-run-2.log
boundary=$(sed -n 's/^max_boundary_diff_tick=\([0-9-]*\).*/\1/p' ./tmp-run-2.log | head -1)
[ "$boundary" -ge 0 ] && [ "$boundary" -le 1 ]

set +e
go run ./cmd/archivecheck -input ./tmp-fixtures/truncated.json -out ./tmp-out \
  > ./tmp-truncated.log 2>&1
trunc_code=$?
go run ./cmd/archivecheck -input ./tmp-fixtures/bad-edit.json -out ./tmp-out \
  > ./tmp-bad-edit.log 2>&1
edit_code=$?
set -e
[ "$trunc_code" -ne 0 ]
[ "$edit_code" -ne 0 ]
grep -q "error_kind=truncated_sample_table" ./tmp-truncated.log
grep -q "error_stage=scan" ./tmp-truncated.log
grep -q "error_kind=invalid_edit_interval" ./tmp-bad-edit.log
grep -q "interval_index=0" ./tmp-bad-edit.log

# go run reports a child non-zero as tool exit 1; verify the contractual code
# directly from the compiled terminal binary.
go build -o ./tmp-out/archivecheck ./cmd/archivecheck
set +e
./tmp-out/archivecheck >/dev/null 2>&1
arg_code=$?
set -e
[ "$arg_code" -eq 2 ]

rm -f ./tmp-out-checkpoint.log ./tmp-run-1.log ./tmp-run-2.log \
      ./tmp-truncated.log ./tmp-bad-edit.log
echo "ACCEPTANCE_OK manifest=$h1 artifacts=$a1 boundary_diff=$boundary"
