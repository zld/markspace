# media-regress

A reusable media processing kernel for published live streams. It normalizes
container metadata, converts audio to a target sample rate / layout /
loudness, streams blocks through a bounded, cancellable queue, and emits a
reproducible publication manifest that can be verified and resumed after an
interruption.

Everything is generated locally: inputs are synthesized fixtures (MP4 and MXF)
and no external service or file is required.

## Requirements

- Go 1.23 (`brew install go@1.23`)
- macOS or Linux, terminal only

```
go mod tidy
go build ./...
go test ./...
```

## Acceptance run

```
go run ./cmd/regress --fixture valid \
  --sample-rate 48000 --layout stereo --loudness -16 --slow-consumer-ms 20
```

Prints (excerpt):

```
stages=5
queue_peak_bytes=32000
manifest_segments=12
continuity_errors=0
sha256=<hex>
manifest_published=true
exit_code=0
```

Other paths:

| Command | Expectation |
| --- | --- |
| `--fixture corrupt --corrupt-block 3` | exit 2, `failed stage=stream`, `track_id`, `input_offset` |
| `--fixture boundary --truncate-timestamp` | `timestamp_errors=1`, no manifest, exit 2 |
| `--fixture gap` | continuity error, exit 3 |
| `--fixture truncated-audio` | `truncated_blocks=1`, verifiable manifest, exit 0 |
| `--format mxf` | same checks over the MXF fixture |

Exit codes: `0` manifest verified, `2` input/processing error, `3` manifest
continuity or digest failure.

## Interrupt and resume

```
go run ./cmd/regress --fixture valid --interrupt-after-block 3 --checkpoint ./tmp/checkpoint.json
go run ./cmd/regress --fixture valid --interrupt-after-block 3 --checkpoint ./tmp/checkpoint.json --resume
```

The resume reports `resume_block=3`, `recomputed_blocks=9`, and produces the
same `manifest_sha256` as an uninterrupted run. The digest deliberately
excludes timing-only summary counters so cache hits and resumes verify equal.

## Content cache

```
go run ./cmd/regress --fixture valid --cache ./tmp/cache          # populates
go run ./cmd/regress --fixture valid --cache ./tmp/cache          # all hits
go run ./cmd/regress --fixture valid --cache ./tmp/cache --tamper-cache
```

`--tamper-cache` corrupts one stored entry before it is read back; the cache
rejects it (`cache_rejected=1`), recomputes the block, and the manifest digest
is unchanged.

## Design

### Time base — `internal/clock`

All media timing is integer ticks in a rational rate (`Num/Den` ticks per
second). The program timebase is 90 kHz. Conversions use exact rational
arithmetic and round half away from zero, so results are identical on every
platform and every run. There is no floating point in timeline arithmetic.

### Errors — `internal/error`

Every stage error carries `Stage`, `TrackID`, `InputOffset`, `Kind` and a
message. Parsers and processors never silently repair input; they return a
structured error naming where and why.

### Containers — `media`, `fixture`, `internal/mxfschema`, `internal/mp4ext`

MP4 is parsed with `github.com/abema/go-mp4`; MXF with a small KLV/BER parser
for a bounded regression profile. Both produce the same `media.Asset` model.

Rotation, clean aperture, pixel aspect, colour and audio parameters are kept
per container description. Normalization applies a documented priority: the
authoritative (first) description wins; later descriptions only fill gaps.
Disagreements are counted per attribute and surface in the manifest summary
(`conflicts`).

A truncated timestamp table (for example an `stts` declaring more entries than
it holds) is rejected as a `timestamp` error — the boundary fixture exercises
this.

### Audio — `audio`

Interleaved s16le PCM. `ChannelMap` up/downmixes named layouts (mono, stereo,
5.1, 7.1); resampling uses integer source-position arithmetic so blocks tile
without gaps or overlaps; output saturates with round-half-away-from-zero.
Integrated loudness is measured over the whole track and turned into a fixed
gain target (silent input and >60 dB correction are refused). A block ending
mid-frame is reported as truncation, never padded or carried across blocks or
tracks.

### Streaming — `stream`

A queue bounded by both block count and media bytes. Producers block when
either bound is reached; the cycle runs the consumer in its own goroutine so a
slow consumer exerts real backpressure. Each block is framed as an RTP packet
(`github.com/pion/rtp`, target-rate timestamp, monotonic sequence, marker on
the last block), re-parsed before enqueue, and decoded/re-validated by the
consumer. Cancellation and timeout stop intake; the last fully committed block
stays recorded. An item larger than the byte bound is rejected as a structured
limit error rather than blocking forever.

### Cache — `cache`

Content-addressed by SHA-256 of (source id, input digest, block index,
normalized processing parameters). A hit is accepted only after the stored
provenance and payload digest both re-verify; any mismatch is a rejection and
forces recompute.

### Checkpoint — `checkpoint`

Per-stage input digest, committed block, timestamp and output digest, plus a
per-block commit log. Written atomically (temp + rename). On load, every
recorded position is validated before any is trusted; an input digest change
or a failed stage restarts from that stage's start.

### Manifest — `manifest`

Deterministic JSON: per-representation windows and render descriptions, the
published audio target, and every segment's ticks, frames and digest.
Canonicalization sorts segments, clears the digest field and excludes the
run summary, so identical inputs and parameters always yield the same
`sha256`. Verification reports numbering jumps, gaps, overlaps, out-of-range
windows, audio-target mismatches and digest tampering without repairing
anything.

### Orchestration — `pipeline`

Runs the five stages (probe, metadata, audio, stream, manifest), owns resume
planning, cache interaction and the reported counters.

## Module layout

```
internal/clock    timebase.go
internal/error    model.go
internal/mp4ext   clap.go, bcrc.go
internal/mxfschema schema.go
media/            track.go, parse.go, mp4*.go, mxf*.go
audio/            layout.go, convert.go, loudness.go
stream/           backpressure.go, pipeline.go, rtp.go
cache/            content.go
checkpoint/       store.go
manifest/         model.go, build.go, verify.go
fixture/          generate.go, mp4.go, mxf.go
pipeline/         run.go, stream.go, commit.go
cmd/regress/      main.go, report.go
```

Each subsystem has its own `*_test.go`.

## Working files

`./tmp/` (checkpoints, cache, built binary) is git-ignored.
