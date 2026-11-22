# mediatimeline

A deterministic, local-only media-timeline pipeline for a remote production
team. It ingests multi-bitrate audio/video tracks, subtitles, chapter marks and
timed metadata (ID3, `emsg`, SEI), repairs unreliable track clocks, recovers
per-domain material independently, expands a ladder, validates segment
boundaries on a shared clock, and emits a reproducible random-access index with
per-segment hashes.

No external services, no sample downloads, no GUI. All inputs are synthesized
from a seed.

## Requirements

- macOS (Darwin) with Go 1.23+
- Go modules fetch two pinned dependencies:
  - `github.com/abema/go-mp4 v1.7.4`
  - `github.com/pion/rtp v1.8.10`

`go-mp4 v1.30.0` does not exist upstream; `v1.7.4` is the newest published
release and is pinned in `go.mod`.

## Build, test, run

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediatimeline demo --seed 42 --faults wrap,loss,color-conflict --ladder 3 --segment-ms 2000
```

The demo prints:

```
roundtrip_match=1.000
keyframe_hit_rate=1.000
segment_hash_match=1.000
recovered_layers=2/3
boundary_errors=0
```

Exit codes:

| code | meaning |
|------|---------|
| 0 | run passed |
| 1 | parameter or runtime error |
| 2 | input unrecoverable, or a boundary/index invariant failed |

`--faults` accepts a comma-separated subset of:
`wrap`, `loss`, `backward`, `color-conflict`, `audio-gap`, `audio-overlap`,
`audio-loss`, `audio-short`, `corrupt-layer`.

Other flags: `--frames`, `--seed`, `--ladder` (1–8), `--segment-ms` (100–60000),
`--json` (machine-readable report).

## Architecture

Data flow: wire/file evidence → parse → shared clock repair → per-domain
recovery (audio / video / metadata) → ladder fan-out → shared-clock fan-in →
index.

| package | responsibility |
|---------|----------------|
| `internal/clock` | exact integer rational time; wrap/backward/origin repair |
| `internal/model` | shared classified error type, samples, events, carriers |
| `internal/ingest` | RTP reassembly, ID3/emsg/SEI/WebVTT/chapter parsing, go-mp4 sample tables |
| `internal/audio` | canonical channel layouts, explicit mappings, rate/segment-frame reconciliation |
| `internal/video` | color declaration resolution, keyframe boundary validation |
| `internal/fanout` | independent ladder layers with per-layer recovery state |
| `internal/fanin` | shared segment plan and boundary tolerance checks |
| `internal/index` | keyframe index and reproducible per-segment hashes |
| `internal/pipeline` | stage orchestration, limits, metrics, exit-code mapping |
| `internal/fixture` | deterministic synthetic MP4, RTP, metadata, and faults |
| `cmd/mediatimeline` | terminal demo |

### Invariants proven by tests

- Ratios are exact: 30 000 additions of 1001/30000 s equal exactly 1001 s.
- RTP 32-bit timestamps wrap by epoch extension; a backward step beyond jitter
  is returned as `track=v-main stage=clock-repair code=backward-timestamp`.
- Reordered packets are forwarded, duplicates recorded, dropped packets counted.
- Audio gaps are concealed, overlaps trimmed, duplicates dropped, and a short
  final block is accepted or rejected explicitly.
- Fan-out layer damage is isolated; the surviving layers are still fully
  indexed (`recovered_layers=2/3`, `unavailable_layers=2`).
- Fan-in refuses an index when layer segment boundaries differ beyond tolerance.
- Keyframe times land on sync-sample boundaries; segment hashes recompute
  byte-identically across runs.

### Limits

- Input: 2 GiB per run (`pipeline.DefaultLimits`).
- Event index memory: 1 GiB.
- Index time precision: within one input track clock tick (all time is exact
  rational arithmetic; no floating point accumulation anywhere in the pipeline).
