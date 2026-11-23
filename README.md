# mediad

Reusable media processing core for distributing Matroska/WebM captures between
edge nodes. It parses EBML byte ranges with provenance, normalizes a nanosecond
timeline, aligns several bitrate renditions onto shared cuts, builds
deterministic shard manifests with per-segment SHA-256, resumes through
checkpoints, and verifies artifacts independently.

Everything runs locally: fixtures are self-generated, no media is re-encoded,
and no network service is contacted.

## Quick start

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediad demo --seed 42 --representations 3 --resume-after 2 --verify
```

The demo prints the acceptance fields and exits 0 only if every invariant
holds:

```
representations=3
ebml_elements=3774
roundtrip_consistency=100.00%
keyframe_hit_rate=100.00%
segments=14
segment_hashes_verified=14
resume_recomputed=0
cache_hits=12 cache_misses=0 cache_quarantined=0
provenance_verified=true
corrupt_cases=5 rejected=5 partial_outputs=0
```

Exit codes: `0` all verified, `2` input or alignment error, `3` cache or
provenance verification failure, `1` internal error.

## Commands

```sh
mediad demo [--seed N] [--representations N] [--resume-after N] [--verify]
            [--work-dir PATH]
mediad verify --manifest PATH --input PATH --provenance PATH
```

`demo` regenerates fixtures from `--seed`, runs the full pipeline, and writes
`capture.webm`, `manifest.json`, `provenance.json` and `checkpoint.json` into the
work directory. `verify` re-reads those artifacts and re-derives every digest
without trusting the producer.

## Layout

| Path | Responsibility |
| --- | --- |
| `cmd/mediad` | CLI entry point, flags and exit-code mapping |
| `internal/ebml` | Streaming EBML reader, VINT codec, block provenance, unknown-size Segments/Clusters |
| `internal/timeline` | Nanosecond clock, wraparound/reversal/sparse diagnostics, rendition alignment |
| `internal/index` | Keyframe index with source spans and tolerance-aware hit reports |
| `internal/segment` | Deterministic manifest builder, per-segment SHA-256, canonical JSON |
| `internal/pipeline` | Checkpointed idempotent runner, content-addressed cache with quarantine, wired into every segment |
| `internal/provenance` | Provenance records and the independent verifier |
| `internal/rtpcheck` | RTP framing boundary validation (pion/rtp) |
| `internal/mp4index` | ISO-BMFF init segment via go-mp4 (timing metadata only) |
| `internal/fixture` | Deterministic capture generator and corruption fixtures |
| `internal/kernel` | Stage wiring, demo driver, corruption suite, exit classes |

## Design notes

- **Streaming.** The EBML reader is forward-only and hashes source bytes as it
  reads them. Only metadata elements are buffered, bounded by
  `MaxMetadataSize`; block payloads are skipped, never held. Memory grows with
  the metadata size, not the input length.
- **No silent loss.** Truncated elements, invalid VINTs and short block payloads
  return a categorized `ParseError` with kind, offset and track. A parse never
  returns a partial document with blocks quietly missing.
- **Determinism.** Keyframe ties resolve to the earlier frame, manifests are
  sorted and serialized in declaration order, and provenance records sort their
  inputs and checkpoints, so identical input and config yield byte-identical
  artifacts.
- **Content-addressed cache on the main path.** Each pending segment is looked
  up in a cache keyed by input digest, segment index and config digest. A hit
  is accepted only after the entry's input provenance, config digest and
  content hash all re-verify, and it carries the authoritative work digest so
  the rebuilt checkpoint matches a fresh computation. Any mismatch quarantines
  the entry and the segment is recomputed; a clean run therefore reports zero
  quarantines. The demo replays post-interruption deliveries into the cache to
  exercise this hit path under `--resume-after`.
- **Boundary correctness.** Segments are half-open `[start,end)`. RTP framing is
  validated per segment: reassembly equality proves no packet spans two shards.
- **Verification is independent.** The verifier re-hashes artifact files, input
  spans, config, manifest and every segment, and re-reads each claimed keyframe
  from the source bytes to confirm the keyframe flag is really set.

## Dependency note

The task specifies `go-mp4 v1.21.2`, but that version does not exist; the latest
published release is `v1.7.4`, which is pinned here. `pion/rtp v1.8.11` is pinned
as specified.
