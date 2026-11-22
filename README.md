# mediapipeline

A deterministic Go media pipeline for segmented audio/video: it parses local
MP4/MOV captures, recovers a synthetic RTP stream over a lossy channel, seals a
watermarked timeline, aligns multi-bitrate representations to shared segment
boundaries, and persists everything through an atomic, idempotent checkpoint.

Everything is generated locally from a seed. No network, device permissions or
external fixture files are involved.

## Requirements

- Go 1.23 or newer (`brew install go`)
- macOS Terminal (no other dependencies)

## Build and test

```sh
go mod download
go build ./...
go test ./...
```

## Acceptance commands

```sh
go mod tidy && go build ./... && go test ./... && \
go run ./cmd/mediapipeline demo \
  --seed 7 --duration 12s --segment 2s --bitrates 400,900,1600 \
  --loss 0.03 --jitter 80ms --late 250ms --interrupt-after 3
```

Reported fields and their acceptance bounds:

| field | meaning | bound |
|---|---|---|
| `av_sync_max_ms` | worst \|video keyframe − audio start\| over shared boundaries | ≤ 40 |
| `packet_recovery_rate` | delivered ÷ (delivered + lost sequence slots) | ≥ 0.85 |
| `p95_latency_ms` | 95th percentile arrival-minus-deadline | ≤ 250 |
| `aligned_boundaries` | number of segments every representation shares | all equal |
| `resume_outputs` | pre/post-resume output content is identical | `true` |
| `manifest_sha256` | stable manifest digest | identical across runs |
| `provenance_records` | segment output keys + summary record | > 0 |

```sh
go run ./cmd/mediapipeline verify \
  --seed 7 --case truncated,zero-timescale,late-event,sequence-wrap,burst-loss
```

`verify` additionally supports `corrupt-sample-table` and
`out-of-order-timestamps`; with no `--case` it runs every fault. It prints a
detection count per case, an aggregate `error_count[<category>]` per unified
error category, and every replayed interval.

Exit codes: `0` all invariants held, `2` input was corrupt and the corruption
was recorded, `1` internal error or a failed invariant.

## Architecture

```
cmd/mediapipeline/main.go        flags, terminal output, exit codes
internal/media/clock.go          exact tick<->nanosecond conversion
internal/media/errors.go         categories, strategies, located errors
internal/container/mp4.go        MP4/MOV box walk and sample table resolution
internal/fixture/                seeded MP4 writer, content and network faults
internal/timeline/watermark.go   event-time watermark, reorder buffer, replay
internal/timeline/replay.go      deterministic recomputation and hashing
internal/rtp/receiver.go         sequence wrap, reorder, bounded loss
internal/segment/align.go        shared keyframe boundaries across bitrates
internal/segment/manifest.go     manifest build, hashing and validation
internal/checkpoint/store.go     atomic state, idempotency keys, ordering
internal/checkpoint/provenance.go input/decision/output tracing
internal/pipeline/               orchestration, invariants, fault scenarios
```

### Data flow

1. **Fixture** writes one MP4 per bitrate and one audio track, using exact
   90 kHz tick arithmetic so keyframes land on identical instants everywhere.
2. **Container** walks boxes strictly: declared extents are checked against the
   file, sample tables are cross-validated, and DTS/PTS are kept in the original
   timescale *and* converted to nanoseconds with overflow detection.
3. **RTP receiver** accepts a seeded loss/jitter/reorder trace starting just
   below the 16-bit wrap point, sorts by extended sequence, and records every
   hole as an explicit gap.
4. **Timeline** seals segments from the event-time watermark, buffers arrivals
   inside the reorder window, and recomputes only the sealed interval a late
   event touches. Empty intervals become visible gap segments rather than being
   skipped or stalling the stream.
5. **Segmentation** intersects keyframe instants across bitrates and snaps each
   target cut back to the latest common keyframe, so all representations share
   identical boundaries.
6. **Checkpointing** writes segments, manifest, provenance and state as temp
   file + fsync + atomic rename, in that order. The checkpoint never names an
   artifact that is not already durable.

### Determinism

- Every stage derives its randomness from `--seed` through `math/rand` sources
  created per component; no global RNG is consulted.
- Timeline events have a total order (`time`, `track`, `seq`, `payload`), so
  arrival order cannot change sealed output.
- Manifests hash real sample payloads and are serialised as canonical JSON.
- The demo runs persistence three times: an optionally interrupted pass, a
  resume pass, and a pure no-op pass that must write zero bytes. Identical
  content hashes across all three are required before exit `0`.

### Bounds and failure policy

| resource | bound | on exceed |
|---|---|---|
| samples per track | 5,000,000 | `invalid_input` with track |
| segments | 10,000 | `invalid_input` |
| events per timeline segment | 100,000 | `replay_limit_exceeded` |
| RTP reorder window | 16 packets (default) | gaps declared and counted |
| replay window | 8 × segment | `replay_limit_exceeded` |
| segment stretch | 3 × target | `boundary_not_found` |

No failure silently drops data: gaps are emitted and counted, corrupt input is
classified with a track/offset/sequence location, and unclassified errors are
reported as `invariant_violation`.

## Output layout

```
.mediapipeline/checkpoint/
  state.json          atomic checkpoint (outputs, boundary, provenance digest)
  manifest.json       canonical manifest
  provenance.json     run-level input/decision/output trace
  segments/<rep>-NNNN.m4s
```
