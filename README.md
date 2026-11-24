# mediaroute

Terminal-only CMAF/fMP4 timeline repair and random-access index rebuild.

Remote production hands edit and audio-post teams multi-track material from
several sources as CMAF/fMP4 fragment groups that may carry **mfhd sequence
gaps**, **tfdt regressions**, **truncated boxes** and **incomplete or corrupt
indexes**. Naive concatenation misaligns tracks; trusting the old index can
skip fragments or seek to the wrong place. `mediaroute` repairs the timestamps,
rebuilds a random-access index that is checked against real box boundaries, and
keeps an auditable record of every error and repair.

## Layout

| Package | Responsibility |
| --- | --- |
| `internal/model` | Unified microsecond time base (exact integer rationals), error taxonomy, fragment/track/index/manifest types. |
| `internal/ingest` | Bounded reading; box length/boundary validation; truncation isolation and moof resynchronisation; moof/moov parsing. |
| `internal/timeline` | mfhd gap/duplicate accounting, tfdt regression repair, RTP timestamp unwrap and cross-check. |
| `internal/index` | Index rebuild from surviving fragments, box-boundary validation, repaired-file delivery. |
| `internal/pipeline` | Bounded-channel stage runner with backpressure, cancellation and locatable error propagation. |
| `internal/report` | Bitrate/quality/alignment metrics, deterministic manifest and standalone index documents. |
| `internal/fixture` | Seeded synthetic fMP4 generation with injectable damage; no external media. |
| `cmd/mediaroute` | Thin CLI: `demo` and `verify`. No processing logic. |

## Requirements

- Go 1.23 or newer (install the macOS package from <https://go.dev/dl/>).
- `github.com/abema/go-mp4 v1.7.4` (the newest v1.30 line does not exist as a
  published tag; v1.7.4 is the pinned, available ISO BMFF parser).
- `github.com/pion/rtp v1.8.9`.

Both libraries are pinned in `go.mod` and fetched by `go mod download`.

## Usage

```sh
./run.sh

# or step by step
go mod tidy && go build ./... && go test ./...
go run ./cmd/mediaroute demo --seed 17 --queue 2 --target-bps 2000000
go run ./cmd/mediaroute verify --input ./tmp/demo-output.mp4 \
    --manifest ./tmp/demo-manifest.json --index ./tmp/demo-index.json
```

`demo` generates five self-contained inputs — clean, sequence gap, tfdt
regression, truncated box, corrupt index — repairs each and prints one JSON
record per case. The clean case is additionally written under the canonical
names `tmp/demo-output.mp4`, `tmp/demo-manifest.json` and `tmp/demo-index.json`
so the `verify` command above can check it. Identical seed and flags produce
byte-identical artifacts and identical digests.

`verify` re-reads the delivered media, rebuilds the index through the same
delivery path, and checks it against the manifest and optional index artifact.
It exits 0 only when every index offset lands on a box boundary, the manifest
consistent, and (when supplied) the index artifact matches. Unrecoverable
input, a missing init segment, a zero timescale, or an inconsistent manifest
all produce a nonzero exit code.

## Behavioural contract

1. **Streaming validation** — a box whose declared length exceeds the remaining
   input, overflows the configured maximum, or is cut off by EOF is recorded
   with its offset and category; the process never crashes on a bad fragment.
2. **Timeline repair** — mfhd gaps are counted but never filled with synthetic
   samples; duplicates are flagged; a regressing tfdt is relocated to the
   previous fragment's end plus its own sample durations. Original value,
   repaired value and source byte range are recorded.
3. **Index rebuild** — an existing sidx/mfra is only a hint. Every offset it
   claims is rechecked against observed box boundaries; the rebuilt index is
   derived independently from valid moof/mdat pairs and sync samples.
4. **Pipeline bounds** — stages exchange results over bounded channels; a full
   queue blocks the producer. Cancellation or a downstream failure stops
   reading and returns a locatable error. Retained media memory is bounded by
   `queue capacity × max box bytes`.
5. **Fault isolation** — a damaged fragment is isolated and later intact
   fragments are processed. Missing init, zero timescale, or a tail whose box
   boundaries cannot be recovered fails the task without emitting a success
   manifest.
6. **Delivery** — the repaired fMP4, a random-access index and a
   machine-readable manifest are written. The manifest lists per-track spans,
   repairs and bad fragments; repeated runs on the same input produce the same
   digest.
7. **Metrics** — target/actual bitrate deviation, valid-sample ratio, dropped
   fragments and manifest-vs-media alignment error are reported. RTP timestamps
   are unwrapped modulo 2^32 onto the same microsecond base.
8. **Coverage** — sequence gap, timestamp regression, truncated box and corrupt
   index fixtures are generated and asserted, including cross-stage error
   propagation and recovery after truncation.

## Conventions

- Media times are track ticks converted to microseconds with exact integer
  rational arithmetic (round-half-up); no floating-point accumulation.
- Compressed sample bytes are never decoded or rewritten. Only fragment
  structure, timestamps and the index change.
- Unknown boxes are preserved byte-for-byte and remain subject to boundary
  validation.
- Output names are confined to `--out-dir`; absolute names, separators and
  `..` components are rejected.
