# mediacheck

A reusable, deterministic media-processing kernel for cross-node regional
live-distribution transport. It analyzes MP4/MOV box structure and keyframe
cadence, localizes damaged regions on a unified nanosecond timeline,
recovers RTP packet loss, and quantifies A/V synchronization — entirely
locally on macOS with no external services.

All sample data is synthesized in-process (`internal/fixture`); the kernel
never reads external media files for its own tests and never rewrites a
source file.

## Toolchain

- Go 1.23 (`brew install go@1.23`); built and verified with the pinned
  module dependencies in `go.mod`.
- `github.com/abema/go-mp4 v1.4.1` — MP4/MOV box and track parsing.
- `github.com/pion/rtp v1.8.12` — RTP packet marshal/unmarshal.

## Build, test, run

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediacheck demo --case all --seed 23 --json
```

Single cases: `normal`, `wrap_drift`, `vfr`, `truncated`, `cross_track`,
`rtp_loss` (or `all`). A local file can be scanned with
`mediacheck scan --file <path> [--json]`.

### Exit codes

| Code | Meaning                                                   |
|------|-----------------------------------------------------------|
| 0    | Success                                                    |
| 2    | Input unparseable (no recognizable MP4/MOV structure)      |
| 3    | Parsed, but one or more regions are unrecoverable          |

## Architecture and data flow

```
container.Scan ──► media timeline ──► track.Graph (topological plan)
      │                   │                    │
      ▼                   ▼                    ▼
 damage.Locate     bitstream.Analyze     pipeline recovery scheduling
      │                   │                    │
      └────────► rtp.Recover ◄─── shared media.TimeBase / Wrapper
                         │
                         ▼
                   sync.Align ──► report.Metrics ──► cmd/mediacheck
```

- `internal/media` — unified nanosecond time base, 32-bit wrap expansion
  (`Wrapper`), least-squares drift estimation, explicit VFR-preserving
  timeline and edit-list mapping.
- `internal/track` — cross-track dependency graph with deterministic
  topological planning; missing dependencies and cycles stop the affected
  tracks with explicit reasons rather than being silently skipped.
- `internal/container` — depth/size-limited raw box pre-scan (truncation and
  `tref` detection) followed by go-mp4 sample-table decoding. Missing or cut
  indexes are marked **undecidable**, never reported complete.
- `internal/bitstream` — keyframe gap distribution, robust MAD-based cadence
  anomaly detection, and a hard distinction between decodable and
  index-inferred boundaries.
- `internal/damage` — the shared structured error model (`Code`, track,
  nanosecond half-open range, recoverability) and range localization/merge.
- `internal/rtp` — sequence-number loss detection, reorder/duplicate
  accounting, retransmission recovery, wrap-safe monotonic timeline and
  loss-robust clock-drift estimation.
- `internal/sync` — nearest-audio A/V alignment; unrecoverable regions stay
  in the statistics as flagged measurements instead of being dropped.
- `internal/report` — deterministic aggregation of every metric, including
  nearest-rank P95 latency.
- `internal/pipeline` — dependency-ordered scheduling over all stages with a
  deterministic per-stage latency model (no wall-clock RNG).
- `cmd/mediacheck` — terminal-only adapter emitting JSON or text.

## Determinism and performance notes

Repeated runs on identical input produce identical timelines, damage ranges
and summaries; fixtures use no randomness. Latency figures are derived from
a deterministic work-unit cost model calibrated for the fixed 10-second
generated assets — they characterize those assets and are **not** real-time
capacity measurements. Input size (512 MiB) and box nesting depth (16) are
bounded, and malformed input returns structured errors.

## Generated demo scenarios

| Case          | Injected condition                                   |
|---------------|------------------------------------------------------|
| normal        | healthy 10 s A/V, zero damage ranges                 |
| wrap_drift    | 32-bit RTP timestamp wrap + 120 ppm clock drift      |
| vfr           | non-constant frame intervals preserved on the line   |
| truncated     | cut `stts` index localized to the track tail         |
| cross_track   | damage on both tracks joined by a `tref` edge        |
| rtp_loss      | 20-packet burst, 19 retransmitted (95% recovery)     |
