# continuity

A reusable local Go library for repairing timestamp discontinuities in
MP4/MOV media after a primary/backup source switch, rebuilding a random-access
sample index, and doing it safely under bounded memory and interruption.

There is no GUI and no network dependency. All fixtures are generated in Go;
the optional FFmpeg script only cross-checks a real decodable stream.

## Requirements

- Go 1.23 (the module declares `go 1.23`)
- FFmpeg 7.1+ only for the optional `scripts/verify-ffmpeg.sh` cross-check

```sh
brew install go@1.23 ffmpeg
```

## Build, test, demo

```sh
go mod tidy
go build ./...
go test ./...

# success case: prints the verification summary JSON
go run ./cmd/continuity demo --work ./tmp/demo --chunk 64 --slow-ms 20

# corrupt case: reports track and sample, exits non-zero
go run ./cmd/continuity demo --case corrupt --work ./tmp/corrupt

# interrupt then resume: second run reuses validated checkpoint blocks
go run ./cmd/continuity demo --case resume --work ./tmp/resume --interrupt-after 2
go run ./cmd/continuity demo --case resume --work ./tmp/resume
```

For a visible process exit code, build the binary first (`go run` masks it):

```sh
go build -o ./tmp/continuity ./cmd/continuity
./tmp/continuity demo --case corrupt --work ./tmp/corrupt; echo $?
```

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | completed |
| 1 | internal or write error |
| 2 | corrupt input |
| 3 | resume state mismatch (changed input or damaged checkpoint) |

## Packages

| Package | Responsibility |
|---------|----------------|
| `internal/clock` | exact rational time base, monotonic repair policy, signed shifts, RTP clock and source-switch planning, shared error model |
| `internal/mux` | deterministic in-Go MP4/MOV fixture writer with structural fault injection |
| `internal/demux` | streaming sample-table reader (timescale, DTS, durations, sync flags, file offsets) with integrity checks |
| `internal/repair` | per-track monotonic timestamp repair and an audit trail of original/repaired values and reasons |
| `internal/index` | time-sorted random-access index, sync-sample lookup, gap errors, checksummed chunk checkpoints |
| `internal/pipeline` | bounded channels, back-pressure, cancellation, slow-consumer and interrupt handling |
| `internal/resume` | checkpoint validation, contiguous-prefix checks, skip-versus-redo policy |
| `internal/manifest` | exact-timeline programme alignment with backward/missing/coverage counters |
| `internal/report` | stable snake_case summary: bitrate, PSNR, alignment error and pipeline metrics |
| `cmd/continuity` | terminal entry point driving the demo scenarios |

## Design notes

- All time arithmetic uses `clock.Rational` (integer numerator/denominator) and
  compares by cross products, so a long programme never accumulates float drift.
- A checkpoint chunk carries its sequence, the 32-byte input fingerprint and a
  SHA-256 checksum of its payload. Writes go to a temp file followed by an
  atomic rename, so an unfinished block is never observed as committed.
- The index records random-access (sync) samples only; `index_entries`
  therefore equals the sync sample count.
- Memory is bounded by the read-queue, chunk-size and pending-write parameters;
  a slow writer blocks the producer through channel capacity rather than
  buffering without limit.
- Recovery refuses to resume when the input fingerprint changes or any chunk
  checksum fails, and reports the offending block.

## Optional FFmpeg cross-check

```sh
./scripts/verify-ffmpeg.sh ./tmp/ffmpeg
```

This generates a real H.264 MP4 with FFmpeg, probes its sample table, decodes
it and reports PSNR. The library's own `go test ./...` remains fully
self-contained.
