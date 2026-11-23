# mediakernel

A reusable Go media-processing kernel for broadcast-grade program acceptance
and repackaging. It parses multi-representation DASH and discontinuous HLS
manifests, recovers and reorders lossy RTP streams on a bounded worker pool,
aligns representations on a single integer time base, and emits a verified
MP4 plus a reproducible audit manifest.

Everything is local and self-contained: fixtures are generated in-process
from an integer seed; no external services or sample files are used.

## Requirements and scope

- Go toolchain. The code uses only Go 1.23 language/library features. The
  `go.mod` directive reads `go 1.24.0` because the pinned dependency
  `github.com/pion/rtp v1.10.6` itself declares `go 1.24.0`; building with an
  older directive is rejected by the Go tool. All first-party sources compile
  and test cleanly at the 1.23 language level.
- First version targets **H.264/AAC only**, in fragmented MP4 and RTP. Other
  codecs are rejected with a structured error rather than half-supported.
- Hard caps are enforced on duration, segment count, packet size, worker
  count and queue depth (`internal/mediaerr/limits.go`). Out-of-range input
  fails explicitly.

## Data flow

```
manifest parse -> unified integer time base -> RTP reorder/dedup
   -> bounded cancellable worker pool -> deterministic merge & alignment
   -> MP4 remux + verify -> atomic publish + hashed audit manifest
```

| Package | Responsibility |
| --- | --- |
| `internal/clock` | Integer rational 90 kHz master clock, overflow-failing conversions, RTP timestamp/sequence unwrap |
| `internal/mediaerr` | Structured errors: category, code, track, timestamp, recoverable flag; hard limits |
| `internal/manifest` | DASH MPD and HLS playlist parsing, timescale/discontinuity/duplicate validation |
| `internal/rtp` | Per-stream dedup, reorder window, exact loss classification, timestamp unwrap |
| `internal/pool` | Bounded worker pool with back pressure and context cancellation propagation |
| `internal/align` | Total deterministic ordering and ordinal-based audio/video alignment |
| `internal/remux` | go-mp4 writer, read-back verification, temp-file + atomic rename publish |
| `internal/fixture` | Deterministic five-class fault generator (dup, reorder, burst loss, bandwidth change, TS wrap) |
| `internal/pipeline` | End-to-end orchestration and acceptance metrics |
| `cmd/audit` | Terminal front-end |
| `testdata/generate.go` | Standalone renderer of fixtures to disk for offline review |

## Run

```sh
go mod tidy
go build ./...
go test ./...

# Normal acceptance run
go run ./cmd/audit --fixture generated --workers 4 --queue 16 \
  --loss-burst 8 --bandwidth-kbps 900,2500,700 --seed 42

# Cancellation: must exit within 1s with no leaked workers
go run ./cmd/audit --fixture generated --case cancel --workers 2 --queue 1

# Damaged input, fully reported
go run ./cmd/audit --case damaged --seed 42
```

`go run` wraps a child's non-zero status as `exit status N` and itself returns
1; build the binary to observe the real process exit codes directly.

## Metrics and exit codes

Output includes `av_sync_error_ms` (target <= 20), `packet_recovery_rate`
(target >= 0.90), `p95_latency_ms` (target <= 500), `unrecovered_packets` and
`manifest_sha256`. Metrics are computed from deterministic integer inputs (no
wall-clock readings feed the hash), so identical inputs hash identically.

| Exit code | Meaning |
| --- | --- |
| 0 | Success |
| 2 | Damaged input, fully reported; no partial manifest committed |
| 1 | Parameter error or internal failure (including cancellation) |

A failed remux writes only a temporary file, verifies it, and renames into
place only on success, so a failed run never leaves a usable manifest.
