# gamepack

Time-aligned, recoverable multi-platform fragment packer for game version
releases. The same set of raw H.264/H.265/AV1 bitstreams and keyframe indexes
is packed into per-platform MP4 fragments on one shared integer media clock,
with a write-ahead log that lets an interrupted or failed run resume or roll
back.

The tool does **not** encode or transcode. It only reads raw bitstreams,
keyframe indexes, fragment manifests and transaction state.

## Requirements

- macOS (Darwin), local only, no external services
- Go 1.23+ (dependencies are pinned in `go.mod`):
  - `github.com/abema/go-mp4 v1.3.0` — fragment muxing and MP4 validation
  - `github.com/pion/rtp v1.8.9` — RTP timestamp/sequence handling

```sh
brew install go
go mod tidy
go build ./...
go test ./...
```

## Layout

| Package | Responsibility |
| --- | --- |
| `internal/clock` | Single 90 kHz integer tick grid; no floating-point media time |
| `internal/errors` | Unified error code + first locatable byte offset |
| `internal/stream` | Annex B / OBU scanning, keyframe index reconciliation, RTP attach |
| `internal/source` | Source-switch and clock-wrap normalization, original coords retained |
| `internal/align` | Common keyframe cut points across bitrate ladder |
| `internal/mp4seg` | MP4 fragment muxing and structural validation |
| `internal/manifest` | Canonical manifest, SHA-256 digests, new/reused/changed diff |
| `internal/txn` | Write-ahead log, same-directory temp files, atomic rename commit |
| `internal/recovery` | Resume position from the last verified stage, leftover cleanup |
| `internal/fixture` | Deterministic synthetic streams/indexes/RTP and fault injection |
| `internal/pipeline` | Stage orchestration wiring the subsystems together |
| `cmd/pack` | Terminal entry point: `demo`, `resume`, `verify` |

## Stages

`scan -> align -> mux -> verify -> commit`

Each transition is fsynced to the WAL before the stage's outputs are durable.
Recovery resumes at the stage following the highest verified stage. A failed
mux/verify rolls fragment outputs back to the last atomic commit point.

## Clock rules

- All stages use the same integer tick grid (90 000 ticks/second).
- A clock wraparound is recognized only when the previous value is in the
  upper half of the configured period and the new value returns to the lower
  half. Any other backward jump is a `CLOCK_BACKWARD` boundary error.
- A declared source switch may restart its RTP clock low; the normalized
  timeline stays monotonic while the original RTP timestamp and byte offset
  are preserved for traceability.

## CLI

```sh
go run ./cmd/pack demo   --root /tmp/job --codecs h264,h265,av1 \
                         --bitrates 500,1000 \
                         --inject switch,wrap,truncated \
                         --crash-after align
go run ./cmd/pack resume --root /tmp/job
go run ./cmd/pack verify --root /tmp/job
```

Exit codes:

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Unclassified/internal error |
| 2 | Input or boundary failure (offset, truncation, index, clock, conflict); no partial commit |
| 3 | Manifest or fragment checksum failure |
| 4 | Simulated crash (run `resume` next) |

## Determinism

Identical inputs produce byte-identical fragments and an identical manifest
digest. A rerun reports `new=0` and `reused=<entry count>`. The input digest
covers the bitstream, keyframe index, RTP source and configuration
fingerprint, so any input change blocks reuse and changes the manifest digest.

## Scripts

- `make accept` / `scripts/verify.sh` — build, vet, test and the full
  crash/resume/verify/truncation/idempotency acceptance sequence.
- `make clean` / `scripts/clean.sh` — remove build artifacts and demo roots.

Build artifacts are git-ignored; only source, tests and scripts are tracked.
Temporary files are created with owner-only (`0600`/`0700`) permissions and
the WAL stores hashes and metadata, never bitstream payloads.
