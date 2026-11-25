# mediaflow

Local, deterministic media processing kernel for broadcast manifest validation,
MP4 scanning, policy/bitrate decisions, chunked repacking, checkpoint recovery
and per-item provenance.

The implementation does **not** decode or encode audiovisual essence. Quality is
a reproducible packaging/bitrate/keyframe statistic, not a subjective quality
claim.

## Build, test and demo

Run in macOS Terminal from the project directory:

```bash
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediactl demo \
  --work ./tmp \
  --budget-mbps 18 \
  --quality-min 0.92 \
  --chunk-samples 64
```

Or use:

```bash
./scripts/verify.sh
```

The demo generates successful, nonzero-start, sparse-keyframe, 8K60 budget,
bad-length, truncated-box, out-of-bounds edit-list and missing-track fixtures.
It then runs:

1. a normal offline batch;
2. a batch with an injected partial-chunk interruption;
3. a recovery rerun using the same batch key;
4. an identical-parameter repeat.

The `demo` command exits 0 when all expected success/failure/recovery assertions
are observed. The lower-level batch command returns the delivery exit code
directly:

```bash
go build -o /tmp/mediactl ./cmd/mediactl
/tmp/mediactl run --work ./tmp --manifest ./tmp/manifest.json
```

Exit codes:

- `0`: all items passed;
- `1`: one or more items were rejected or failed quality/budget/chunk checks;
- `2`: manifest or command parameters were invalid.

Note: `go run` itself may normalize a non-zero program status to its own exit
status 1; build `mediactl` when asserting exact delivery exit codes.

## Packages

- `internal/clock`: media/RTP time bases, wraparound and edit-list mapping using `github.com/pion/rtp`.
- `internal/fault`: stable error classes and located track/sample/byte/time intervals.
- `internal/probe`: manifest validation plus bounded MP4 box/sample-table scanning and `github.com/abema/go-mp4` cross-reading.
- `internal/policy`: quality proxy, resolution/frame-rate/quality/budget decisions and deviations.
- `internal/plan`: canonical explainable configuration and stable configuration digest.
- `internal/repack`: streaming checksummed chunks, self-describing chunk metadata and final assembly.
- `internal/index`: atomic local checkpoint index, compatibility checks and incomplete-chunk rollback.
- `internal/batch`: failure-preserving offline orchestration.
- `internal/audit`: per-item provenance, text/JSON summaries and stable summary digest.
- `internal/fixture`: deterministic local MP4 success, boundary and corruption generators.

All generated and processed paths are confined below `--work`; absolute paths and
`..` traversal are rejected.
