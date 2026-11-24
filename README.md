# mediapack

A reusable, local media-processing kernel for regional live-streaming
distribution. It demuxes Matroska/WebM material that may contain unknown-length
EBML elements, interleaved multi-track blocks, truncated clusters, damaged
segments or incomplete indexes; remuxes it losslessly; and lets offline batch
jobs resume from checkpoints with numeric verification.

Everything runs locally on macOS with no external services. Sample media is
generated deterministically from a seed — no external files are referenced.

## Build and test

```sh
go mod tidy
go build ./...
go test ./...
```

Or run the bundled scripts:

```sh
./scripts/verify.sh       # tidy + build + vet + full test suite + acceptance summary
./scripts/acceptance.sh   # end-to-end CLI flows and exit-code assertions
```

## Command line

```sh
# Generate valid and corrupted samples, print block counts, track mapping and
# payload SHA-256. Sample generation succeeds with exit code 0.
go run ./cmd/mediapack demo --seed 7 --out /tmp/mediapack-demo

# Process one input with checkpointed, resumable batching.
go run ./cmd/mediapack run --input <file.webm> --index <dir> \
  [--interrupt-after N] [--corrupt-index]
```

Exit codes (compiled binary; `go run` may wrap non-zero codes on some
toolchains):

| Code | Meaning |
|------|---------|
| 0 | success |
| 2 | corrupted / unprocessable input (truncation, oversize length, timestamp regression, unsupported codec, input changed) |
| 3 | corrupt checkpoint index |

## Data flow

1. **EBML read + cluster scan** (`internal/ebml`) parses unknown-length
   Segment/Cluster elements and records the byte interval, track number and
   timestamp of every readable block. Illegal VINTs, out-of-range lengths and
   tail truncation return structured faults carrying an absolute offset; bytes
   are never skipped silently.
2. **Unified timebase + error model** (`internal/clock`, `internal/fault`) are
   shared by demux, mapping and mux. All timestamps are nanoseconds; invalid or
   regressing timestamps fault rather than being guessed/repaired.
3. **Demux** (`internal/demux`) supports VP8/VP9 video, Opus/Vorbis audio and
   WebVTT subtitles, preserving raw block payloads and stream order. An
   unsupported codec faults with its input position.
4. **Track mapping** (`internal/track`) assigns stable 1-based output track
   numbers from input order; the mapping is published in the digest.
5. **Lossless remux** (`internal/mux`) rebuilds WebM or MP4 container structure
   only — never transcoding — and hashes each block's payload on both sides.
   MP4 output is structurally re-read with `go-mp4`.
6. **Checkpoint** (`internal/checkpoint`) commits fixed-size block batches with
   input size, committed offset, block count and checksum. A corrupt index or
   changed input refuses resume via a recoverable, offset-bearing fault.
7. **Batch runner** (`internal/batch`) resumes strictly after the last
   validated checkpoint, so re-runs do not re-read completed input.
8. **RTP boundary** (`internal/rtp`) validates sequence continuity and payload
   boundaries, reporting duplicate/jump packets with sequence number and media
   offset, without altering remux payloads.
9. **Digest** (`internal/verify`) emits block counts, the track mapping,
   per-track and aggregate payload SHA-256, and the fault location.

An independent decoder (`internal/crosscheck`, built on `github.com/at-wat/ebml-go`)
acts as an oracle: on intact streams it must agree with the internal parser on
block payloads and timestamps. The internal parser remains authoritative for
byte-offset fault localization, which the oracle cannot provide.

## Module layout

| Package | Responsibility |
|---------|----------------|
| `internal/fault` | cross-module structured error model with offsets |
| `internal/clock` | nanosecond timebase and monotonicity monitor |
| `internal/ebml` | offset-aware EBML reader, cluster/block scanner, metadata |
| `internal/fixture` | deterministic WebM sample + corruption generator |
| `internal/track` | codec whitelist and stable track mapping |
| `internal/demux` | multi-format demultiplexer to nanosecond packets |
| `internal/mux` | lossless WebM/MP4 remuxer with per-block hashes |
| `internal/checkpoint` | batched local index and resume-safety checks |
| `internal/batch` | resumable offline runner |
| `internal/rtp` | RTP sequence and payload-boundary verification |
| `internal/verify` | deterministic digest report |
| `internal/crosscheck` | ebml-go reference-decoder agreement |
| `internal/acceptance` | numeric acceptance report (10 cases, 5 categories) |
| `cmd/mediapack` | terminal entry point |

## Bounds and non-goals

* Local files only; memory grows with the configured block batch limit.
* Damaged payloads are not repaired and missing media is not reconstructed.
* When reliable parsing is impossible past a fault location, processing of that
  input stops and the already-committed checkpoints are retained.
* Build artifacts are not part of the source delivery.
