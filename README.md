# mediatool — local media processing kernel

A reusable, dependency-light local kernel for long-term media archive work.
It unifies inconsistent audio specifications, rebuilds a trustworthy
timeline in the presence of clock drift, timestamp wraparound, VFR and
packet loss, and resumes interrupted batch runs without redoing committed
work.

Everything runs locally on macOS (Darwin). The kernel itself depends only
on two Go libraries; FFmpeg is used solely to produce/verify optional
acceptance material and is never invoked from the processing path.

## Layout

```
cmd/verify            acceptance entry point (--generate / --resume)
internal/errors       stable error codes + byte offsets, exit-code mapping
internal/clock        integer-ns timeline, timescale math, wrap/drift
internal/container    MP4/MOV track & sample scanning (go-mp4)
internal/rtprecover   per-SSRC dedup, reorder, explicit loss (pion/rtp)
internal/audio        PCM resampling, channel mapping, loudness, stats
internal/index        atomic local chunk index with stable digest
internal/batch        chunk checkpoint, verify, crash-safe resume
internal/fixture      deterministic synthetic media (no external files)
internal/pipeline     orchestration shared by tests and acceptance
scripts               build.sh, accept.sh
```

## Time and error model

* All crossing-boundary time is integer nanoseconds since a zero media
  origin, plus explicit sample ticks with their timescale. No float places
  samples.
* 32/16-bit counters are unwrapped only when the modular delta lies inside
  the half-range window; an unprovable jump is a `wrap_broken` error.
* Timelines reject any backwards PTS/DTS (`time_went_back`); VFR content is
  positioned by its real PTS via per-sample intervals.
* Every recoverable failure is a `MediaError` with a stable `Code` and, for
  container faults, the byte offset. Codes map to process exit codes:

  | exit | meaning                                  |
  |------|------------------------------------------|
  | 0    | success                                  |
  | 2    | corrupted input (container/audio)       |
  | 3    | checkpoint or index inconsistency        |
  | 1    | any other failure                        |

## RTP recovery

Per SSRC, packets are deduplicated (counted then dropped), reordered inside
a bounded window, and never fabricated. A position is declared lost only
when the window overflows or at finalization; late packets arriving after
their slot has slid away are rejected with `late_outside_window`. Gaps that
fill inside the window count as recovered; genuinely dropped bursts remain
explicitly lost.

## Crash safety

Chunk output is written to a `.tmp` file and atomically renamed. The index
and checkpoint are likewise temp-file-then-rename (with fsync). A chunk is
only marked committed after its output hash verifies, so a checkpoint can
never point at partial data. On resume, committed chunks are re-verified and
skipped; any output/index mismatch aborts at the first inconsistent chunk
with exit code 3.

## Build and test

```sh
brew install ffmpeg        # optional; enables one real-MOV integration test
go mod download
go build ./...
go test -race ./...
```

## Acceptance

```sh
go run ./cmd/verify --generate --out .tmp/accept --chunk 2s
go run ./cmd/verify --resume  --out .tmp/accept
# or, end to end:
./scripts/accept.sh
```

The generator emits normal, wraparound, drift, VFR, duplicate/reorder,
burst-loss and truncated fixtures and prints JSON containing
`sync_error_ms`, `loss_recovery_rate`, `latency_p95_ms`,
`duplicate_packets`, `recovered_chunks`, `index_sha256` and `exit_code`.
The truncated fixture exits 2 with a byte offset; wrap/VFR never move time
backwards; the resumed index digest equals the first run's. All fixtures and
indices live under the project temporary directory and are safe to delete.

## Scope

Local MP4/MOV and the bundled RTP fixtures only. Undecodable content is
recorded, not repaired; per-chunk memory is bounded and unrecoverable
ranges are reported rather than hidden.
