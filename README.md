# replay

Offline regression kernel for real-time voice RTP traces. It ingests local
RTP/RTCP captures, normalises them onto one nanosecond timeline, maps
versioned MP4 media assets onto tracks, merges the versions into a
deterministic plan, and publishes a hashed manifest with provenance.

Everything runs locally. No network access, no external files: the demo
scenarios synthesise their RTP, RTCP and MP4 inputs in memory.

## Toolchain note

The task specified Go 1.23, but the pinned dependency `github.com/pion/rtp
v1.10.6` itself declares `go 1.24.0`, so `go.mod` must declare `go 1.24.0`
or newer (the local toolchain is Go 1.27). The library versions are exactly
as specified:

- `github.com/abema/go-mp4 v1.7.2`
- `github.com/pion/rtp v1.10.6`
- `github.com/pion/rtcp v1.2.15` (sender-report cross-check)

## Commands

```
go mod tidy
go build ./...
go test ./...

go run ./cmd/replay demo --case normal --versions v1,v2
go run ./cmd/replay demo --case truncated-rtcp
go run ./cmd/replay demo --case conflict
go run ./cmd/replay demo --case resume --stop-after map
go run ./cmd/replay demo --case resume --resume
go run ./cmd/replay verify --workdir .replay/<case>
```

Exit codes: `0` manifest verified (or a requested stop), `1` input or merge
failure, `2` argument error.

## Stages

| stage    | package              | behaviour |
|----------|----------------------|-----------|
| ingest   | `internal/ingest`    | RTP reorder with 16-bit unwrap, duplicates counted once, late-window packets become deficits, RTCP sender reports parsed with exact truncation offsets |
| timeline | `internal/clock`     | RTP → nanoseconds, RTCP or first-packet anchor, wrap-aware, regression and overflow rejected |
| graph    | `internal/graph`     | explicit dependency graph, stable topological order, cycles fail before any publish |
| map      | `internal/mapasset`  | version assets mapped to track intervals; equal content merges, differing content quarantines with both hashes, holes report source versions |
| merge    | `internal/merge`     | all-or-nothing plan publication in dependency order; rollback removes downstream artefacts |
| manifest | `internal/manifest`  | content-hashed manifest, source records, verification and diffing |

Each completed stage persists its input hash, output hash and recovery
position atomically in `.replay/<case>/checkpoint.json`. `--resume` restarts
at the first incomplete stage; a changed input rolls that stage and every
downstream stage back and records why.

## Scenarios

- `normal` — clean two-track, two-version run; repeated runs reproduce the
  same manifest hash and report `0 change(s)`.
- `seq-wrap` — audio sequence numbers cross the 16-bit boundary once.
- `conflict` — a duplicate with different payload and a v1/v2 asset content
  mismatch; reports at least one quarantined conflict.
- `truncated-rtcp` — a sender report cut short; reports the byte offset and
  exits non-zero.
- `dependency-cycle` — audio and video depend on each other; the graph stage
  fails and no plan or manifest is published.
- `resume` — the normal pipeline under its own workspace for stop/resume
  acceptance.

## Output

Artefacts live under `.replay/<case>/`: generated `assets/`, stage
JSON (`ingest.json`, `timeline.json`, `graph.json`, `map.json`),
`out/plan.json`, `manifest.json`, `checkpoint.json` and `baseline.json`.
Payload bytes are never written and never appear in error messages; only
lengths and SHA-256 digests are recorded.
