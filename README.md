# media-check

A reusable Go media-processing kernel for a broadcast ingest pipeline: MP4/MOV
probing, lossless remux, keyframe indexing, RTP receive integrity handling, and
a bounded worker pipeline that emits auditable evidence for every output.

Everything is local: no decoders, no transcoders, no network services. Fixtures
are generated in-process, so the demo never reads a file the repository does not
create.

## Requirements

* macOS (Apple Silicon or Intel) or any OS with Go
* Go 1.23

```sh
brew install go
go mod tidy
go build ./...
go test ./...
```

## Demo

```sh
go run ./cmd/mediacheck demo --seed 41 --workers 4 --queue 8 --out /tmp/media-check
go run ./cmd/mediacheck verify --input /tmp/media-check/source.mov --output /tmp/media-check/remux.mp4
```

The `demo` command writes `source.mov`, emits `remux.mp4`, and writes
`manifest.json` plus per-segment artifacts under `/tmp/media-check`. The
terminal output is the acceptance summary:

```
roundtrip_parse=100.00%
keyframe_hit=100.00%
segment_sha256_match=100.00%
tracks=2
segments=8
duplicates=3
reordered=5
lost=2
cancelled_tasks=4
repeat_manifest_match=true
```

Additional modes:

```sh
go run ./cmd/mediacheck rtp --window 4          # show gap records only
go run ./cmd/mediacheck demo -v --out /tmp/x    # add hashes, gaps, bandwidth
```

### Exit codes

| code | meaning |
| ---- | ------- |
| 0    | success |
| 2    | corrupt input or a failed invariant |
| 64   | command-line usage error |
| 130  | interrupted by SIGINT/SIGTERM |

## Architecture

| file | subsystem |
| ---- | --------- |
| `media/timebase.go`  | timescale conversion with 128-bit overflow checks |
| `media/errors.go`    | shared error classes plus offset/track/segment/packet context |
| `media/probe.go`     | strict box scanner and structural summary |
| `media/sample.go`    | sample-table expansion and boundary validation |
| `media/trackmap.go`  | track identification, ordered target mapping, conflicts |
| `media/remux.go`     | atomic lossless rewrite with byte-level verification |
| `media/index.go`     | keyframe index and random-access hit verification |
| `transport/rtp.go`   | RTP parse, 16→64-bit sequence extension, dedup |
| `transport/reorder.go` | bounded reorder window and gap accounting |
| `transport/adapt.go` | sliding-window bandwidth controller |
| `pipeline/pool.go`   | bounded pool with backpressure and cancellation |
| `pipeline/merge.go`  | deterministic merge and manifest invariants |
| `pipeline/run.go`    | producer/consumer chain, cancellation, resume |
| `pipeline/fusion.go` | transport report fused into the media manifest |
| `cmd/mediacheck`     | acceptance and demo adapter |

### Data flow

```
RTP datagrams ─► Parse ─► SeqExtender ─► Dedup ─► Reorderer ─► ordered packets
                                                              └► gap records ─┐
                                                                              ▼
MP4/MOV ─► ScanBoxes ─► ProbeFile ─► BuildTrackMap ─► SplitSegments ─► Pool ─► Merger
                                                                              │
                                                            keyframe index ──►└─► manifest.json
```

The receive side and the media side share one timebase convention (integer
ticks, checked rescale) and one error taxonomy, so a timebase failure, a
corrupt box offset, or an RTP gap all surface with the same class and context.

## Guarantees

* **Lossless remux** — compressed sample bytes, durations, decode order,
  composition offsets and sync flags are preserved for `avc1` and `mp4a`.
  Any unknown required box is rejected explicitly rather than ignored.
* **Atomic output** — samples are written to a temp file in the output
  directory, re-probed, compared byte for byte against the source, and only
  then renamed into place. A failure removes the temp file and leaves the
  input untouched.
* **Path confinement** — every write path is validated against the requested
  output directory; `..` escapes are rejected.
* **Bounded resources** — input is capped at 20 GiB, the reorder window and
  worker queue are configurable with hard ceilings, and the pool applies
  backpressure when full.
* **Deterministic evidence** — the manifest digest covers content evidence
  (per-segment hashes, track tables, RTP gaps, keyframe index summary) but not
  execution provenance, so the same input yields the same digest regardless of
  worker count, completion order, or output directory.
* **Resume** — a durable ledger records verified segments keyed by source
  digest. A re-run skips segments whose artifact still verifies, and recomputes
  everything when the input changed or an artifact is incomplete.

## Invariants

The merged manifest enforces, across stage boundaries:

* per-track sample-count conservation,
* decode-time monotonicity and non-negative composition time,
* contiguous segment boundaries with continued DTS,
* failure isolation — a failed or cancelled segment never carries a payload
  hash, so it cannot be mistaken for verified content.

## Fault fixtures

`media.Corrupt` generates at least five fault classes from a valid fixture:

| fault | expected class |
| ----- | -------------- |
| truncated box | `box_bounds` / `truncated` |
| invalid sample offset | `invalid_sample` |
| unknown required box | `unsupported_box` |
| zero timescale | `timebase` |
| duplicate sync sample | `invalid_sample` |
| chunk/sample mismatch | `invalid_sample` |

RTP duplicates, reordering, and an out-of-window burst loss are produced by
`transport.DefaultRTPDrill`; mid-processing cancellation is exercised by the
`cancel-drill` step of the demo and by `pipeline` tests.

## Scope

Only ISO-BMFF MP4/MOV and RTP packets are handled. There is no video decoding,
transcoding, rendering, or playout. The lossless promise is limited to the
supported codecs and box combinations named above.
