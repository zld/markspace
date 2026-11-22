# mediasync

A reusable, fully local media-processing kernel for remote production
teams. It demuxes Matroska/WebM captures from heterogeneous devices,
normalizes their clocks onto one nanosecond time base, recovers lossy RTP
delivery, processes samples on a bounded worker pool, and losslessly
re-packages the result with independently verifiable synchronization and
recovery evidence.

No codec payload is ever decoded or re-encoded, and no external service is
contacted. All fixtures are generated in memory from a seed.

## Build, test and demo

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediasync demo \
  --seed 41 --tracks video,audio,subtitle \
  --drift-ppm 180 --wrap-at 900000 --loss 0.02 \
  --workers 4 --queue 16
```

Exit codes:

| code | meaning |
|------|---------|
| 0 | all invariants passed |
| 2 | damaged input or a track mapping error |
| 3 | internal processing or cancellation-propagation failure |

## Architecture and data flow

```
EBML parser ─▶ demuxer ─┬─▶ shared clock timeline (wrap/drift/monotonicity)
                        └─▶ RTP recoverer (reorder/dedupe/windowed loss)
                                   │
                         bounded worker pool (back-pressure, cancellation)
                                   │
                  deterministic merge (timestamp, track, seq, index)
                                   │
              lossless remux (WebM + MP4 sidecar, atomic publish)
                                   │
                       report (independent cross-stage reconciliation)
```

- `internal/ebml` — streaming EBML reader supporting unknown-length
  Segment/Cluster, VINT codec, depth/size limits and truncation diagnostics.
- `internal/demux` — Matroska header/block demux and explicit track mapping
  validated before any output is written.
- `internal/clock` — per-track clocks reduced to one nanosecond base:
  modulo unwrapping, non-zero origins, static ppm drift correction, abrupt
  drift detection, and monotonicity violations that are never silently fixed.
- `internal/rtprecover` — bounded jitter buffer over `pion/rtp`: reordering,
  de-duplication, in-window recovery and aged-out gaps reported through the
  shared error taxonomy.
- `internal/pipeline` — fixed-size worker pool with a bounded queue,
  end-to-end context cancellation, and a total-order deterministic merge.
- `internal/remux` — deterministic WebM writer with atomic temp-file
  publication and payload digests, plus a go-mp4 MP4 sidecar that carries
  the same verbatim payloads.
- `internal/report` — recomputes p95 sync error, recovery rate and p95
  latency and cross-checks input/output summaries (`ErrInvariant`).
- `internal/testmedia` — deterministic WebM and impaired-RTP fixture
  generators plus fault-injection mutations.
- `internal/app` — orchestration core and the damaged-fixture suite.
- `cmd/mediasync` — thin terminal adapter.

## Safety limits

Inputs are bounded at every layer: element size and nesting depth in the
EBML reader, container size in the demuxer and remuxer, worker count and
queue depth in the pipeline, and RTP recovery-window size in the recoverer.
Oversized data is rejected rather than trusted.

## Supported codecs

Lossless remux is limited to what WebM/MP4 can carry here: VP8, VP9, Opus,
Vorbis and WebVTT. Other codecs fail mapping validation rather than being
written into a container that cannot represent them.

## Dependency note

The brief names `github.com/ebitengine/go-mp4@v1.4.1`, but that module path
does not exist on GitHub or the Go module proxy (HTTP 404). The MP4 support
uses `github.com/abema/go-mp4@v1.4.1`, the same-version, actively available
box library that provides the VP8/VP9/Opus box types and streaming
`StartBox`/`EndBox` writer this project requires. `github.com/pion/rtp` is
pinned at the specified `v1.8.11`. Both versions are fixed in `go.mod`.
