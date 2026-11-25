# medialab

A reusable, fully local media-processing kernel for regional live
distribution nodes. It analyzes MXF/MP4 tracks and RTP streams, locates
damaged sections, and produces checkable recovery results after
interruption or retry. No external services or sample files are used:
all inputs are synthesized deterministically from an integer seed.

## Requirements

- macOS (Darwin), Go 1.23
- Dependencies (pinned in `go.mod`):
  - `github.com/abema/go-mp4 v1.4.1` — MP4 box parsing
  - `github.com/pion/rtp v1.8.11` — RTP packet parsing
- MXF parsing uses the Go standard library only (SMPTE 336M KLV/BER)

## Build and test

```sh
brew install go@1.23
export PATH="$(brew --prefix go@1.23)/bin:$PATH"
go mod tidy
go build ./...
go test ./...
```

## Demo

```sh
go run ./cmd/medialab demo \
  --seed 7 --loss-burst 4 --reorder 3 --duplicate 2 \
  --bandwidth 800,300,900 --interrupt-after commit-1
```

The command generates clean, damaged and restarted samples, then prints
for each: `av_sync_ms`, `recovery_rate`, `p95_latency_ms`,
`keyframe_gap_ms`, `damage_ranges`, `conflict_count`, `commit_id`.
Exit code 0 means every assertion passed; non-zero means a parse,
recovery or assertion failure.

## Architecture

```
cmd/medialab        terminal entry point, drives the local library only
internal/timeline   single 48kHz integer master clock, rational math,
                    16/32-bit wrap-safe counters
internal/model      located error records (offset, track, time window,
                    stable error code) and damage-range merging
internal/fixture    deterministic PRNG, MP4/MXF/RTP sample generators and
                    corruption injectors (truncation, invalid timestamp,
                    missing index, loss/reorder/duplicate)
internal/container  stdlib MXF KLV reader, go-mp4 reader, MXF > MP4 >
                    default metadata arbitration with conflict markers
internal/stream     frame scan, keyframe gaps, incomplete-frame rules and
                    located damage detection, A/V sync measurement
internal/rtp        bounded reorder/dedup buffer, wrap-safe monotonic
                    timestamps, bandwidth-adaptive bounded wait, XOR
                    single-parity recovery
internal/txn        fsynced write-ahead journal and atomic stage commit;
                    replay rolls back uncommitted stages
internal/report     deterministic summary, stable JSON and content hash
internal/pipeline   wires the subsystems into four transactional stages
```

## Key contracts

- All container/RTP timestamps convert to one integer tick scale.
- Boundary-incomplete frames never count as valid keyframes.
- Reorder memory and the duplicate window are bounded; a packet outside
  the window is recorded as late/lost, never waited on indefinitely.
- Timestamp wrap advances a monotonic axis; time never runs backwards.
- Buffer wait is clamped to a finite ceiling that adapts to bandwidth.
- XOR parity reconstructs single-hole groups; multi-hole groups are
  reported unrecoverable rather than guessed.
- Each stage is journaled before an atomic commit. After a crash the
  previous durable boundary is preserved, uncommitted work is rolled
  back, and re-running yields the same commit id and summary hash.
- Only stream structure is reported; unknown codecs are never claimed
  to be decoded.
