# assetcheck

A local, terminal-only processing kernel for validating offline
multi-language game assets. It checks MP4 containers, track and fragment
constraints, and an accompanying RTP delivery stream, and it locates every
inconsistency at a byte offset in the original input.

The problem it addresses: when dialogue audio, subtitles and sound effects
each compute their own timeline, assets disagree by amounts that are hard to
explain, and nobody can point at the bytes that caused the disagreement.
`assetcheck` puts every subsystem on one nanosecond clock, derives one
digest-bound index, and reports drift, gaps and corruption with the exact
input offset that produced them.

## Requirements

- macOS (Darwin) with a Go 1.23 toolchain
- No network services; everything runs locally
- Dependencies pinned in `go.mod`:
  - `github.com/abema/go-mp4 v1.3.0` — MP4 box and track index parsing
  - `github.com/pion/rtp v1.8.11` — RTP sequence and header parsing

```sh
go mod download
go mod tidy
go build ./...
go test ./...
```

## Commands

```sh
# Generate every demo asset from a seed, check it and print reports.
go run ./cmd/assetcheck demo --seed 7 --out /tmp/assetcheck-demo

# Re-run against the indexes written next to the inputs.
go run ./cmd/assetcheck demo --seed 7 --out /tmp/assetcheck-demo --reuse-index

# Check explicit files (optionally reusing a bound index).
go run ./cmd/assetcheck check --mp4 asset.mp4 --rtp packets.rtp --index asset.index.json --reuse-index
```

Exit codes:

| code | meaning |
| ---- | ------- |
| 0 | every asset passed |
| 1 | at least one asset failed validation |
| 2 | usage or run error (bad flags, unreadable input, fixture mismatch) |

The demo writes, per scenario, `asset.mp4`, `packets.rtp` and
`assetcheck.index.json` under `<out>/<scenario>/`. With `--reuse-index` the
on-disk inputs are left alone so you can edit one and watch the index be
rejected and rebuilt.

## Scenarios the demo generates

| scenario | expectation |
| -------- | ----------- |
| `normal` | pass, zero time and byte deviation on every sample |
| `duplicate` | pass, one duplicate packet with a zero time increment |
| `missing` | fail, exactly one missing packet and a retained time gap |
| `truncated` | fail, a truncated Opus packet reported at a byte offset |
| `corrupt` | fail, a corrupt fragment reported at a byte offset |
| `wrap` | pass, the RTP sequence crosses the 16-bit wrap point intact |

Fixtures are fully generated from `--seed`; no media is checked in.

## Architecture

```
cmd/assetcheck         demo and check commands, exit codes, index reuse
internal/clock         the single nanosecond base and exact unit conversion
internal/fault         one located error type: stage, byte offset, time, cause
internal/index         digest-bound index, persistence, structural checks
internal/scan          MP4 box walk, fragment materialisation, RTP events
internal/codec         Opus TOC and AAC ADTS duration mapping
internal/rtp           wrap-safe extended sequence, duplicates, gaps
internal/validate      fragment/sample constraints, mapping, subtitle, skew
internal/stream        confirmed-first execution of an index
internal/report        terminal rendering and the stable summary digest
internal/fixture       deterministic test media generation
```

Data flow: `scan` reads the MP4 and packet stream and emits an `index` bound
to an input digest; `validate` checks track, fragment and subtitle
constraints and computes per-sample drift; `stream` executes the index and
keeps only confirmed records; `report` renders the mapping, faults and
digests and decides the verdict.

All three subsystems have independent tests: `internal/scan/scan_test.go`
(clock, sequence, codec, scanner) and `internal/stream/stream_test.go`
(pipeline, digest binding, reproducibility).

## Reporting

Every fault names its stage, byte offset, media time and cause:

```
stage=codec code=codec.opus.truncated offset=49 time=20000000ns: opus packet is shorter than its TOC frame layout (need 2 bytes, have 1)
```

The per-sample mapping shows the container offset, the matched packet, and
the byte and time deviation. A clean asset shows `0` on every row.

### Digest algorithms

- Input digest: `sha256/v1`. The index stores a SHA-256 over a
  domain-separated concatenation of both inputs (`assetcheck/input/v1`, then
  a length-prefixed MP4 and a length-prefixed packet stream). Reusing an
  index whose digest no longer matches is refused and the scan is rebuilt.
- Report summary digest: `fnv1a64/v1`. FNV-1a 64 over a canonical,
  newline-separated field list (input digest, track/fragment/packet counts,
  every mapping row, duplicate/missing/skew/subtitle totals, stream totals,
  every fault's stage/code/offset/time/sample, and the verdict). Paths and
  wall-clock values are excluded, so identical inputs produce byte-identical
  reports and the same digest.

## Time base

All times are `clock.Nanos`, an int64 nanosecond offset. Conversions from a
timescale use exact 128-bit intermediate arithmetic and round half away from
zero, so an MP4 track at 48 kHz, an RTP clock at 48 kHz and subtitle ticks
map onto one base without a second rounding convention.

## Guarantees and deliberate limits

- RTP sequence numbers are extended to 64 bits across the 16-bit wrap.
  Duplicates never advance media time; a gap records exactly how many
  packets are missing and no audio sample is fabricated for it.
- Opus durations come from the TOC byte; AAC durations come from the ADTS
  header, falling back to the container frame size (marked `inferred`).
- A truncated packet, an abnormal duration and a non-monotonic timestamp
  each produce a located fault at the packet's offset.
- The index is bound to its input digest. A changed input invalidates it and
  triggers a rebuild, which is reported explicitly.
- Repeated runs over the same input produce the same mapping, the same fault
  order and the same summary digest.
- Streaming keeps confirmed records when it hits a failure and never counts
  a failed sample as confirmed.

Out of scope by design: repairing damaged media, decoding audio, and any
network or GUI dependency. Memory grows with the number of index records
(packet events, samples and fragments); the index is held in memory and
persisted as JSON.
