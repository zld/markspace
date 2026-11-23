# mediaforensics — offline archive restore kernel

A reusable Go media-processing kernel for a digital-forensics archive
workflow. It synthesizes multi-track timelines, repairs damaged containers,
recovers lossy RTP transport and exports an offscreen, hash-verifiable frame
sequence. Every anomalous output can be traced back to an input byte offset.

Everything is generated in-process: there are no external media files, no
network calls and no GUI dependencies.

## Requirements

- Go 1.23+ (developed and tested on macOS/Darwin)
- Pinned dependencies in `go.mod`:
  - `github.com/abema/go-mp4 v1.3.0` — MP4 box header parsing
  - `github.com/pion/rtp v1.8.10` — RTP packet parsing and sequence handling
  - standard library only otherwise (`testing`, `crypto/sha256`, `encoding/json`)

## Run

```sh
go mod tidy && go build ./... && go test ./...
go run ./cmd/restore --self-test --seed 42 --frames 120 \
  --packet-loss 0.08 --output /tmp/archive-restore
```

Or `./scripts/run-demo.sh` (override with `SEED`, `FRAMES`, `LOSS`, `OUT`).

### Terminal contract

Each fixture case prints:

```
case=<name> offset=<byte offset> recovered_rate=<fraction> \
     sync_error_ms=<ms> p95_latency_ms=<ms>
```

The summary line is:

```
status=ok frames=<n> sync_error_ms<=20.0 recovered_rate>=0.90 \
     p95_latency_ms<=50.0 manifest_sha256=<64 hex chars>
```

Exit codes (process-level):

| Code | Meaning |
| ---- | ------- |
| 0    | Every self-test path satisfies all quantitative thresholds (`status=ok`). Damage that was recovered per policy still exits 0; each case's `run-manifest.json` keeps its own `exit_code=2` as auditable evidence. |
| 2    | A single-input run was damaged but recovered per policy; all evidence written and all thresholds still met (reserved for single-input mode). |
| 1    | An invariant or quantitative threshold failed (`status=failed`). |

## Architecture and data flow

```
fixture ──► parse ──► model ◄── clock
             │         │
             ├─► repair│
             │         ▼
             │      render ──► PPM frames + per-frame SHA-256 manifest
             ▼
           rtp (recover) ──► rtpstats
             │                 │
             └────────► validate ◄── render / repair
                          │
                       manifest (stage evidence + run checksum)
```

- `internal/fixture` — deterministic synthetic MP4 subset (ftyp/moov/mdat, two
  video tracks + one audio track), damage variants (truncated box, out-of-range
  index, corrupt sample, missing index, clock overflow) and damaged RTP streams.
- `internal/parse` — strict per-box walker (length, nesting depth ≤ 16,
  boundary checks) and index rebuild via MFSM payload-header boundary scan.
  Errors carry stage, track and absolute byte offset; nothing is silently skipped.
- `internal/clock` — exact rational time arithmetic with explicit overflow;
  no floating-point accumulation inside timeline math.
- `internal/model` — unified timeline/track/sample model; overflow blocks the
  affected track while preserving its first source offset.
- `internal/repair` — coalesces damaged samples into regions, applies a
  one-sample safety margin and never lets damaged bytes reach the valid timeline.
- `internal/rtp` — bounded playout-window recovery. Reordered in-window packets
  count as recovered; duplicates are dropped and never counted as recovery;
  gaps surviving the window are unrecoverable.
- `internal/render` — headless P6 PPM raster output, deterministic two-track
  transition blends with full source provenance, explicit gap frames.
- `internal/validate` — A/V sync on the shared rational time base, recovery and
  latency thresholds, source-offset propagation for every failure.
- `internal/manifest` — per-stage JSON evidence, self-checksumming run manifest
  and path-traversal rejection.
- `cmd/restore` — terminal entry point wiring all stages together.

## Determinism and auditability

Repeated runs over identical inputs produce byte-identical frame hashes and
the same ordered error summary. Each rendered frame's manifest record lists
the source track, source sample index, source byte offset, output time and
transition blend weight, so an anomaly can be traced from pixels to bytes.

## Safety constraints

- Single input capped at 256 MiB.
- Box nesting depth capped at 16.
- Output frames capped at 10,000.
- Out-of-bounds allocation and output path traversal are rejected.
- Only the code-generated MP4 subset and the conventional RTP header are
  supported; missing media payloads are never invented.
