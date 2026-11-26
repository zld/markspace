# CMAF/fMP4 archive verification

Local, deterministic Go 1.23 project for scanning CMAF/fMP4 fragments, building a
microsecond time base, recovering RTP traces, and reading half-open index ranges.
It has no external service dependency.

## Run acceptance checks

```bash
go mod tidy
go build ./...
go test ./...

go run ./cmd/verify -case clean -range 2s:3s \
  -iterations 100 -mem 8MiB -queue 4

go run ./cmd/verify -case faults -range 2s:3s \
  -iterations 100 -mem 8MiB -queue 4 -resume -repeat 2
```

Or run `./scripts/acceptance.sh`.

Generated fixtures and atomic index/checkpoint files are written under
`.verify-work/`; generated media is created by code and does not reference
external files.

## Terminal output

The verifier emits a JSON object containing:

- `sync_ms`: maximum event-anchor A/V-RTP synchronization error.
- `loss_recovery_pct`: matching retransmissions divided by detected losses.
- `p95_ms`: p95 half-open range-read latency.
- `peak_mib`: peak accounted pipeline cache occupancy.
- `mfhd_gaps`: missing mfhd sequence intervals.
- `tfdt_repairs`: backward/wraparound timestamp repair count.
- `quarantined`: truncated/out-of-bounds fragments excluded from the index.
- `offset_trace`: original raw timestamp, fixed raw timestamp, microsecond
  offset, source, track and tfdt box offset.
- `digest_equal`: repeated/resumed index digest equality.

The fault fixture contains one mfhd gap, one same-source small tfdt regression,
out-of-order and duplicate RTP packets, four lost RTP packets with three matching
retransmissions, one truncated box, and a slow consumer.

## Exit codes

Build a local binary for exact process exit status:

```bash
go build -o .verify-work/verify ./cmd/verify
./.verify-work/verify -case clean ...      # 0
./.verify-work/verify -case faults ...     # 0 when injected faults are recovered
./.verify-work/verify -case unparseable    # 2
./.verify-work/verify -case limit          # 3
./.verify-work/verify -case cancel         # 3
```

`go run` itself may normalize a non-zero child status to its own status `1` and
print the child's exit status; the compiled binary preserves 2/3 directly.

## Layout

- `internal/cmaf`: bounded box scanner, init/moof/mfhd/traf/tfdt/trun/mdat parsing.
- `internal/timebase`: microsecond conversion, wraparound/source/regression repair.
- `internal/rtp`: pion/rtp-backed trace parsing, reordering and retransmission recovery.
- `internal/index`: deterministic index, atomic persistence, resume and range reads.
- `internal/stream`: bounded queue/cache pipeline, backpressure and checkpoints.
- `internal/fixture`: deterministic valid and fault-injected dual-track media.
- `cmd/verify`: terminal acceptance entry point.

All modules use `model.Us` and `model.MediaError` for shared time units and
source/track/box-offset/original/fixed-time provenance.
