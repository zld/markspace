# tsrepair

A reusable local media-processing kernel for cross-node MPEG-TS distribution.
It repairs timestamps, rebuilds random-access indexes, and verifies recovery
stability with per-segment results. Pure Go, no external services, no GUI; all
behaviour is driven through a terminal CLI and library APIs.

## Requirements and dependency note

- Go 1.23 toolchain (the `go` directive in `go.mod` is `1.23`; it also builds
  on newer local toolchains).
- `github.com/pion/rtp v1.8.12` — RTP encapsulation and sequence checks.
- `github.com/abema/go-mp4` — MP4 box encode/decode for index output.

> **Version deviation:** the task brief asked for `go-mp4 v1.16.0`, but that
> release does not exist upstream. The latest published tag at authoring time
> is **v1.7.4**, which is what `go.mod` pins. All required box
> (`ftyp/moov/mvhd/trak/tkhd/mdia/mdhd/hdlr/minf/stbl/stsd/stts/stss/stsz/
> stco/mdat`) marshalling and round-trip parsing are available in v1.7.4.

## Layout

```
cmd/tsrepair          acceptance CLI: demo / verify
internal/mediaerr     stable error codes, severity, PID/offset locators
internal/clock        rational time base, PCR unwrap/drift, PTS/DTS, VFR
internal/ts           188-byte packet parse, CC checks, PSI, PES, CRC-32
internal/transport    pion/rtp encapsulation and 16-bit sequence checks
internal/segment      deterministic canonical encoding and SHA-256
internal/index        random-access index builder and hit/miss accounting
internal/fanout       independent per-representation clock/index recovery
internal/fanin        shared-clock boundary cross-check (1 ms tolerance)
internal/mp4index     go-mp4 serialization and strict round-trip verify
internal/fixture      deterministic in-memory TS stream + corruption modes
internal/pipeline     streaming orchestration wiring every subsystem
```

## Build and test

```sh
go mod tidy
go build ./...
go test ./...
go vet ./...
```

## Acceptance commands

Repair a generated stream set (three bitrates, twelve segments each, with
counter-gap, PCR-wrap and PSI-version faults):

```sh
go run ./cmd/tsrepair demo \
  --seed 17 --segments 12 --representations 3 \
  --corrupt counter-gap,pcr-wrap,psi-version \
  --output /tmp/tsrepair-a

go run ./cmd/tsrepair verify --input /tmp/tsrepair-a

go run ./cmd/tsrepair demo \
  --seed 17 --segments 12 --representations 3 \
  --corrupt counter-gap,pcr-wrap,psi-version \
  --output /tmp/tsrepair-b

diff -r /tmp/tsrepair-a /tmp/tsrepair-b
```

A clean run prints `parse_roundtrip=100%`, `index_hit_rate=100%`,
`boundary_mismatch=0`, the expected `segment_hashes` count, and
`deterministic=true`. `verify` reports every segment SHA-256 as matching, and
the two demo directories are byte-identical.

Recoverable structural faults:

```sh
go run ./cmd/tsrepair demo \
  --corrupt truncated-packet,missing-pcr-anchor,psi-missing-section \
  --output /tmp/tsrepair-c
```

Each fault lists its stable error code, PID/byte offset and recovery action.
Recoverable errors exit `0`; structurally unrecoverable streams exit `2`; any
round-trip field mismatch or hash difference exits `1`.

## Design guarantees

- **Bounded streaming memory.** The TS reader keeps a small resync buffer; the
  PSI section assembler rejects oversize sections (`MaxSectionLength`); the
  pre-anchor sample queue is capped; no subsystem loads the whole stream.
- **No silent corruption.** Sync loss, adaptation overflow, CC gap/repeat/jump
  and truncated packets all carry stable codes and input byte offsets. A bad
  packet is skipped as a whole 188-byte unit, never treated as continuous data.
- **Atomic PSI switching.** A new PAT/PMT version takes effect only after every
  section is present and CRC-valid; rollbacks, missing sections and CRC errors
  keep the previous complete mapping.
- **Clock safety.** PCR (42-bit/27 MHz) and PTS/DTS (33-bit/90 kHz) wrap
  expansion uses exact integer arithmetic with overflow detection; drift
  correction is clamped per step (default 1 ms) and outliers are isolated.
- **VFR preserved.** Sample intervals come from actual PTS deltas; no fixed
  frame-rate grid is imposed.
- **Independent representations.** Each bitrate owns its clock, collector and
  index; failed representations are excluded from successful results.
- **Strict index semantics.** A hit means the returned sample's half-open time
  range contains the target; out-of-range targets are counted as `index_miss`,
  never reported as hits.
- **Determinism.** Segment canonicalization is length-prefixed big-endian, so
  identical inputs always produce identical SHA-256 digests regardless of map
  iteration order or struct padding.
