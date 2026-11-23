# mediatool

A reusable Go media-processing library that aggregates CMAF/fMP4
multi-track fragments from remote capture and recording devices into
synchronized, post-production-ready material packages — without decoding
or re-encoding any compressed media.

It is built for imperfect real-world input: missing `mfhd` sequence
numbers, `tfdt` rollback or wraparound, truncated boxes, drifting device
clocks, wrapping RTP timestamps, variable video frame rates and
late-arriving fragments that invalidate already-built timelines.

## Requirements

- Go 1.23+
- Dependencies pinned in `go.mod`: `github.com/abema/go-mp4 v1.3.0`,
  `github.com/pion/rtp v1.8.11`

## Quick start

```sh
go mod tidy
go build ./...
go test ./...

# End-to-end deterministic demo
go run ./cmd/mediatool demo --seed 42 --fragments 12 --late-at 7 --out /tmp/media-demo

# Re-verify a produced package (reproducible summary hash)
go run ./cmd/mediatool verify --dir /tmp/media-demo
```

Exit codes: `0` success, `1` input/processing failure, `2` argument error.

## Demo output keys

| key | meaning |
| --- | --- |
| `target_bitrate_bps` | configured aggregate target bitrate |
| `actual_bitrate_bps` | bitrate measured from remuxed payload bytes |
| `bitrate_deviation_pct` | signed deviation, acceptance target \|x\| <= 5% |
| `sample_hash_match` | every input payload hash matches its output chunk |
| `quality_samples` | total compressed samples accounted for |
| `manifest_alignment_ms` | worst video/audio fragment start-time difference |
| `late_recomputed` | events re-ordered by deterministic backtracking |
| `rejected_boxes` | malformed boxes rejected with locatable errors |

## Architecture and data flow

```
fragment bytes
   -> internal/mp4        incremental box scan + moof/mfhd/traf/tfdt/trun parse
   -> internal/rtp        32-bit timestamp unwrap, wrap/restart split, drift
   -> internal/timeline   shared time base, watermark, late detection, recompute
   -> internal/mapping    explicit track-id/type/codec rules with diagnostics
   -> internal/remux      disk-spooled lossless remux + deterministic manifest
   -> internal/metrics    bitrate deviation, hashes, alignment
   -> cmd/mediatool       terminal demo / verify driver
```

### Subsystems

- **`internal/mp4`** — streaming, fault-tolerant box parser. Every box
  records its absolute byte offset; 32- and 64-bit lengths are supported;
  oversize, truncated and illegal-length boxes return classified errors.
  The abema/go-mp4 reader runs as an independent structural cross-check.
- **`internal/rtp`** — expands 32-bit RTP timestamps onto a monotonic
  64-bit clock (legal wrap vs. device restart) and fits capture drift with
  a bounded-window least-squares estimator.
- **`internal/timeline`** — the shared media time base, unified error
  taxonomy, per-track event watermark and deterministic recomputation
  (ordered by corrected DTS, tie-broken by input order).
- **`internal/mapping`** — explicit rules bind tracks to named output
  slots; ambiguity, missing tracks and codec conflicts stop only the
  affected output and retain a diagnosis.
- **`internal/remux`** — compressed payloads are spooled to disk as they
  arrive and re-emitted byte-identically; init segments, contiguous
  chunk numbering and a content-hashed manifest are rebuilt. Path writes
  are confined to the output root.
- **`internal/metrics`** — computes bitrate deviation, verifies payload
  hashes and measures cross-track alignment from the manifest alone.
- **`internal/fixture`** — generates all sample data deterministically
  from a seed: valid, corrupt and boundary fragments, RTP wraparound and
  late delivery. No external files are referenced.

## Error classification

All failures use one stable class from `internal/timeline`:
`box_truncated`, `box_length`, `field_missing`, `timestamp_backward`,
`timestamp_wraparound`, `duplicate_sequence`, `late_fragment`,
`mapping_conflict`, `overflow`, `unsafe_path`. Each carries a byte offset
or time and a human-readable detail string.

## Determinism and memory

Repeated runs with identical inputs produce identical event order,
manifests and summary hashes. The streaming parser buffers at most one
fragment (bounded at 4 MiB) and the remuxer spools payloads to disk, so
steady-state memory does not grow with total input volume; this is
covered by the 1,000-fragment tests.
