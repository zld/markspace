# avcheck — realtime media kernel

A reusable Go media-processing kernel for multi-track interactive sessions:
capture frames arrive as RTP packets over a lossy, reordering network, are
repaired and validated, composed onto one monotonic rational timeline, and
exported offscreen as PNG frames plus a reproducible SHA-256 manifest.

Everything is local and deterministic. Fixtures are generated from a seed;
no external media files or services are used.

## Layout

| Path | Responsibility |
| --- | --- |
| `internal/media` | Frame model, rational cross-track clock, classified errors |
| `internal/transport` | RTP metadata subset, dedup/reorder/window, bandwidth estimate |
| `internal/ingest` | Self-generated sequence validation, MP4 sample tables, keyframe index |
| `internal/timeline` | Track state, batch and streaming composers, placeholder rules |
| `internal/render` | PNG export, per-segment and per-frame SHA-256, manifest |
| `internal/pipeline` | Bounded metered queues, backpressure, cancellation |
| `internal/session` | Fault injection and the end-to-end run |
| `cmd/avcheck` | `demo` and `verify` entry points |

## Pipeline

```
capture ──▶ transport ──▶ ingest ──▶ timeline ──▶ render
 (queue)      (queue)                  (queue)      (files)
```

Each arrow is a fixed-capacity `pipeline.Queue`. Memory is bounded by the
queue capacity plus one reorder window; nothing grows with the number of
frames. Wire frames are compact identity payloads, so full RGBA rasters are
only materialized at export time.

### Determinism

* Capture is a pure function of `(seed, track, frame)`.
* Fault injection is index-based, so a seed reproduces the fault pattern.
* The PNG encoder uses filter 0 at a fixed zlib level.
* The manifest digest covers its body but not its location, so a rerun in a
  different directory compares equal.

## Acceptance

```sh
brew install go@1.23
go mod tidy
go build ./...
go test ./...
go run ./cmd/avcheck demo --seed 41 --frames 1200 --tracks 3 --queue 32 \
  --faults duplicate,reorder,burst-loss,bandwidth-shift,corrupt,truncated \
  --out /tmp/avcheck-run
go run ./cmd/avcheck verify --input /tmp/avcheck-run
```

The demo prints `frames_in`, `tracks`, `duplicates_dropped`,
`reordered_recovered`, `burst_gaps`, `corrupt_rejected`,
`truncated_rejected`, `queue_peak`, `roundtrip_consistency`,
`keyframe_index_hit_rate`, `segments_verified`, `segment_sha256_match` and
`rerun_manifest_match`.

Exit codes:

* `demo`: `0` when the run completes and the export is self-consistent,
  `1` on an argument or unrecoverable structural error.
* `verify`: `0` clean input, `2` input was corrupt but recovered under
  policy, `1` on a structural error or an export that fails its manifest.

Note: `go run` reports any non-zero child status as `1` (it prints
`exit status 2`). Build the binary with `go build -o avcheck ./cmd/avcheck`
when the exact exit code matters.

### Export directory

```
manifest.json          # mirrored from png/, frames relative to png/
run.json               # configuration needed to reproduce the run
summary.json           # machine-readable acceptance fields
mp4/track_NN.mp4       # per-track sample table and keyframe index
png/frame_NNNNN.png    # exported frames
png/manifest.json      # authoritative manifest next to the frames
```

## Fault classes

| Fault | Injected | Detected / handled |
| --- | --- | --- |
| duplicate | repeat a packet | dedup ring, counted, never re-emitted |
| reorder | hold a packet past a later one | reorder window, recovered before commit |
| burst-loss | drop a run of packets | gap records above threshold, placeholder slots |
| bandwidth-shift | vary arrival spacing | estimator retunes the reorder window only |
| corrupt | flip a payload bit | SHA-256 mismatch, frame rejected with track/seq |
| truncated | shorten the payload | length mismatch, frame rejected with track/seq |

## Policies

`--policy skip` (default) drops a corrupt frame and keeps the track alive,
so the slot becomes a placeholder. `--policy fail-track` marks the track
failed and stops feeding it; its MP4 index is not written.
