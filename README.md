# mediasync

A reusable Go library that turns unstable multi-camera RTP/RTCP packet streams
into a synchronized, randomly-accessible media representation for editing.

It handles packet loss, reordering, device clock drift, 32-bit RTP timestamp
wrap and variable frame rate together, rebuilds random-access MP4 indexes,
aligns multi-bitrate rendition boundaries and quantifies the recovery result.
All sample media is synthesized locally from a fixed seed; no external files,
devices or services are required.

## Layout

| Package | Responsibility |
|---|---|
| `internal/clock` | Shared clock model: sequence distance, 32-bit unwrap, RTCP-based drift estimation, bounded limits and the cross-subsystem error classification |
| `internal/rtp` | Bounded RTP parsing/reordering (loss, duplicate, late, out-of-window) and self-contained RTCP Sender Report parsing |
| `internal/repair` | Per-rendition monotonic media timeline, truncated/backwards sample isolation, VFR duration analysis and segment grid |
| `internal/fixture` | Deterministic H.264/H.265 RTP/RTCP generator with drift, VFR, wrap, loss, reorder, duplication and truncation |
| `internal/fanout` | Independent per-rendition recovery, SR drift fitting and keyframe boundary alignment |
| `internal/index` | Bounded MP4 writer with random-access sample tables and an independent re-parse verification path |
| `internal/manifest` | Frame-bounded multi-rendition segment alignment with switch-point rejection |
| `internal/metrics` | Deterministic report: bitrate error, loss/recovery, PSNR, manifest error and index hashes |
| `cmd/mediasync` | Terminal-only demonstration and acceptance driver |

## Build and test

```sh
go mod tidy
go build ./...
go test ./...
```

## Acceptance demos

```sh
# Success path (three renditions)
go run ./cmd/mediasync demo --seed 41 --duration 30s \
  --bitrates 1200000,2400000,4800000 --out /tmp/mediasync-demo

# Structural damage path (uses the default three-rendition ladder)
go run ./cmd/mediasync demo --seed 41 --case corrupt --out /tmp/mediasync-corrupt
```

Other scenarios: `--case clean|loss|wrap|corrupt`.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success, including corruption that was safely isolated and indexed around |
| 2 | Invalid command-line arguments |
| 3 | Unrecoverable structural damage |

## Determinism

A fixed seed reproduces byte-identical MP4 artifacts and identical report
summaries, including `index_sha256`. All parse lengths, queue depths and index
offsets are bounded and checked for integer overflow via the shared clock
limits; the independent MP4 re-parse guarantees no index entry escapes the
`mdat` payload bounds.
