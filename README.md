# mediamerge

A reusable, purely local media-merge kernel. It reconciles multiple versions of
UTF-8 SRT/WebVTT subtitles, MP4 sample/keyframe indexes and RTP timestamp
sidebands onto one production timeline, using a single 1/90000 second integer
timebase. No media is decoded or transcoded; no external service is contacted.

## Build and test

```sh
go mod tidy
go build ./...
go test ./...
```

A single command runs every acceptance scenario and asserts the documented
metrics and exit codes:

```sh
./scripts/acceptance.sh
```

## Terminal commands

```sh
go run ./cmd/mediamerge verify --case normal   --out /tmp/pwf-normal
go run ./cmd/mediamerge verify --case damaged  --policy mark-gaps --out /tmp/pwf-damaged
go run ./cmd/mediamerge verify --case conflict --policy strict   --out /tmp/pwf-conflict
go run ./cmd/mediamerge verify --case recovery --interrupt-after 2 --out /tmp/pwf-recovery
go run ./cmd/mediamerge resume --out /tmp/pwf-recovery
```

Exit-code contract implemented by the program:

| Code | Meaning |
|------|---------|
| 0 | completed, or damage recorded under the active policy (incl. an interrupted run awaiting resume) |
| 1 | local read/write, checksum or state failure |
| 2 | input rejected by the rules (encoding, timecode, bounds, conflict, ...) |

> Note on `go run`: this machine's `go run` (verified under both go1.23.12 and
> go1.24.0, in clean `bash` and `zsh`) reports a child program's non-zero exit
> as its own exit `1` while printing `exit status 2`. The mediamerge binary
> itself returns the documented code `2`; build it with
> `go build -o mediamerge ./cmd/mediamerge` to observe `2` directly. The
> acceptance script builds the binary for this reason.

## Package layout

| Package | Responsibility |
|---------|----------------|
| `internal/fault` | unified staged, line/offset-attributed error model |
| `internal/clock` | 90kHz integer ticks, checked arithmetic, 24/25/30000-1001 DF/NDF timecode |
| `internal/subtitle` | UTF-8 SRT/WebVTT parsing, strict and lenient modes, DF label rejection |
| `internal/track` | canonical cue / gap / rejected / timeline model |
| `internal/mapping` | explicit version+track mapping table with offsets |
| `internal/merge` | identical-overlap dedupe, content conflict, provenance gaps |
| `internal/mp4idx` | bounded MP4 sample-table + sync-sample index (go-mp4), no mdat loading |
| `internal/rtpclock` | RTP sequence dup/reorder/loss and 32-bit timestamp unwrap (pion/rtp) |
| `internal/cas` | SHA-256 content-addressed derivative cache, source-scoped invalidation |
| `internal/txn` | write-ahead journal, per-segment hash verify, atomic manifest, rollback/replay |
| `internal/report` | deterministic terminal + JSON report |
| `internal/fixture` | in-code generation of all sample assets and damage variants |
| `internal/pipeline` | end-to-end orchestration and crash recovery |
| `cmd/mediamerge` | terminal adapter |

## Invariants

- Source files are opened read-only and identified by SHA-256.
- Cache keys bind source digest + normalization version + parameters, so a
  changed source only invalidates its own derivatives.
- Box/sample offsets are validated against real file size; nothing scans past
  a declared box end, and integer overflow is rejected, never silently wrapped.
- Repeated runs of the same fixture produce identical per-segment hashes.
