# mediacheck

A reusable, purely local Go media-processing kernel for verifying multi-bitrate
DASH representations and HLS playlists (including `EXT-X-DISCONTINUITY`) at
edge distribution nodes. It maps every manifest onto one exact integer time
base, locates damaged segments, validates channel/sample-rate/color metadata
and commits results through a write-ahead journal with deterministic,
hash-stable reports.

No external media services or sample files are required: all manifests, fMP4
segments and RTP packets are synthesized in-process from a numeric seed.

## Toolchain and dependencies

- Go 1.23 (the module's `go` directive is pinned to `1.23`).
- `github.com/abema/go-mp4` — ISO BMFF/fMP4 parsing and marshalling.
- `github.com/pion/rtp` — RTP packet marshal/unmarshal.

> Note on versions: the brief requested `go-mp4 v1.28.0`, but that tag does not
> exist upstream or on the module proxy. The latest published release is
> `v1.7.4`, which is therefore pinned in `go.mod`. `pion/rtp v1.8.13` is pinned
> exactly as requested.

## Layout

```
cmd/mediacheck      terminal demo entry point (exit 0/1/2)
internal/clock      unified 90 kHz integer time base, exact rational maps
internal/model      cross-module fault kinds, stages, sentinel errors
internal/safeio     parse/report memory caps, path containment, duration bounds
internal/fixture    deterministic media synthesis (fMP4 + RTP) and fault injection
internal/dash       MPD and fMP4 init/media segment parsing
internal/hls        M3U8 parser with discontinuity sequences
internal/probe      keyframe cadence, RTP loss, damaged-range isolation
internal/normalize  channel layout / sample rate / color conflict validation
internal/txn        write-ahead journal, staged validation, atomic commit
internal/report     deterministic single-line JSON metrics and SHA-256
scripts             local acceptance runner
```

## Build, test and demo

```sh
go mod tidy
go build ./...
go test ./...

go run ./cmd/mediacheck demo --seed 17 --faults none
go run ./cmd/mediacheck demo --seed 17 --faults truncate,late,metadata
go run ./cmd/mediacheck demo --seed 17 --faults truncate,late,metadata --crash-after validate --resume
```

A single JSON object is emitted per run with `target_bitrate_bps`,
`actual_bitrate_bps`, `bitrate_deviation_pct`, `quality_mean`, `quality_p95`,
`manifest_alignment_ms`, `damaged_ranges`, `metadata_conflicts`,
`recovered_transactions` and `report_sha256`.

Exit codes: `0` analysis and commit completed cleanly; `2` damage detected but
the report was produced successfully; `1` invalid input or transaction failure.

The full acceptance sequence (including hash equality between a resumed run and
an uninterrupted rerun) is available via:

```sh
./scripts/acceptance.sh
```

## Determinism and recovery guarantees

- All time conversion is integer/rational arithmetic; zero timescales, overflow
  and values that cannot be represented losslessly are rejected.
- The journal is fsynced before visibility; a crash leaves only a durable
  `prepared` record. Resume rolls orphaned prepares back and commits exactly
  once — replay never double-commits.
- The report hash excludes run-state (the recovery counter), so a recovered run
  and an uninterrupted rerun produce an identical `report_sha256`.
