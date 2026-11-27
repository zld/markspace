# mediacheck

A reusable, dependency-light Go media-processing kernel for local elementary
streams. It parses H.264/H.265 Annex B NAL units and AV1 length-prefixed OBUs,
verifies an external keyframe index, computes keyframe intervals, locates
corrupt byte ranges, and supports bounded streaming plus verifiable
checkpoint/recovery. It never decodes, transcodes, repairs or fetches media.

## Requirements

- Go 1.23 (tested through Go 1.27)
- No services, no network at runtime; sample data is generated in code.
- Direct dependencies are pinned in `go.mod`:
  `github.com/abema/go-mp4 v1.2.0`, `github.com/pion/rtp v1.8.11`.

## Layout

```
internal/timeline   integer media time bases, overflow-checked rescale
internal/errors     unified stage/track/byte-range/time-range error model
internal/stream     bounded chunked reader + slow-consumer back-pressure
internal/codec      H264/H265 Annex B NAL and AV1 OBU streaming parsers
internal/index      keyframe-index reconciliation, RTP and MP4 adapters
internal/txn        write-ahead log + atomic checkpoint commit/recovery
internal/pipeline   stage wiring, segment hashing, demo generator, reports
cmd/mediacheck      terminal adapter emitting machine-readable JSON
```

## Build and test

```sh
go mod tidy
go build ./...
go test ./...
go test -race ./internal/stream ./internal/txn ./internal/pipeline
```

## Demo (self-generated data, no external files)

```sh
# interrupt after the scan stage, then resume to completion
go run ./cmd/mediacheck demo --codec all --chunk-bytes 4096 --queue 2 \
  --faults truncated,invalid-length --interrupt-after-stage scan \
  --out ./.mediacheck/run1 --json
go run ./cmd/mediacheck demo --codec all --chunk-bytes 4096 --queue 2 \
  --faults truncated,invalid-length --resume \
  --out ./.mediacheck/run1 --json
```

The JSON envelope contains `roundtrip_consistency`,
`keyframe_index_hit_rate`, `segment_sha256_count`, `damage_ranges`,
`peak_buffer_bytes`, `recovered` and `replayed_committed_stages`.
On the acceptance route: consistency is `1.000`, hit rate `>= 0.95`,
segment hashes are identical across reruns, damage ranges carry valid
offsets, and `peak_buffer_bytes <= chunk_bytes*(queue+2)`.

## Exit codes

The kernel returns:

| code | meaning                                   |
|------|-------------------------------------------|
| 0    | completed and summary verified            |
| 2    | input or index error                      |
| 3    | commit or recovery verification failure   |

Observe them through the built binary, because `go run` collapses every
non-zero program exit to `1` (Go toolchain behavior, unchanged across 1.23):

```sh
go build -o mediacheck ./cmd/mediacheck
./mediacheck demo --codec bogus --out /tmp/x ; echo $?   # 2
./mediacheck demo --codec h264 --faults none --out /tmp/y --json ; echo $?  # 0
```

## Fault model

- `truncated` cuts the final framing boundary (Annex B dangling start code;
  length-prefixed short payload) and is reported as a `parse.truncated-tail`.
- `invalid-length` overwrites an interior length prefix; the scanner resyncs
  to the next valid unit and emits a clamped `parse.invalid-length` gap that
  never extends over later valid data.
- Both faults together (`--faults truncated,invalid-length`) produce two
  independent damage ranges in a length-prefixed (AV1) stream.
- Context cancellation while the bounded queue is full unblocks the producer
  and exits every goroutine.

## Bounds

Input size, queue capacity, chunk/segment size, index entry count, WAL size
and reassembled unit size all have hard caps. The terminal adapter only reads
the inputs it is given and only writes inside the specified output directory.
