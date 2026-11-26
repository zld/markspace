# replaykit

Local, deterministic Go media-processing kernel for sports-replay clipping.

It cuts highlight intervals defined by event nanosecond timestamps, fans them
out to several bitrate fMP4 renditions on a **shared rational clock**, ingests
length-prefixed RTP audio (Opus or AAC), reports every defect as one ordered
anomaly manifest (bitrate layer, input byte offset, media-time range, category,
severity and recovery action), and resumes from content-addressed checkpoints
by rebuilding only the segments whose hash no longer verifies.

No network or external media files: every sample is generated in-process by
`internal/fixture`. Targets macOS `darwin/arm64` and `darwin/amd64`.

## Toolchain

The acceptance wrapper is Go 1.23.12:

```sh
brew install go
go install golang.org/dl/go1.23.12@latest
go1.23.12 download
go1.23.12 mod tidy
go1.23.12 mod download
```

`github.com/pion/rtp v1.10.6` declares `go 1.24.0`, so with `GOTOOLCHAIN=auto`
the `go1.23.12` wrapper transparently selects the cached Go 1.24 toolchain
while the documented commands stay unchanged. `go.mod` pins
`github.com/abema/go-mp4 v1.7.4` and `github.com/pion/rtp v1.10.6`.

## Acceptance

```sh
go1.23.12 build ./...
go1.23.12 test ./...
go1.23.12 run ./cmd/replay selftest --case all --out /tmp/replay-proof
```

Or run the whole chain including the 0/1/2 exit-status checks:

```sh
./scripts/selftest.sh
```

`selftest --case all` regenerates the fixtures and runs every scenario. It
prints the stable counters and writes `/tmp/replay-proof/proof.json` plus, per
case, a `manifest.json`, a `checkpoint.json` and a `segments/` tree.

### Verified counters

* normal clean AAC+Opus world: `roundtrip=100%`, `index_hits=100%`,
  `boundary_skew_ticks=0`, `segments=6` (3 events × 2 bitrate layers),
  `hash_match=6/6`, `anomalies=0`.
* fault worlds report exactly `lost=1`, `duplicate=1`, `truncated=1`,
  `index_rebuilt=1`, and every recovered output hash matches.
* hash-failure recovery rerun: `rebuilt=1`, `reused=5`,
  `duplicate_products=0`, `hash_match=6/6`.

### Exit status

| Code | Meaning |
| ---: | --- |
| `0` | selftest passed, including recoverable fault cases |
| `1` | fatal input (e.g. no time mapping) or output verification/selftest failure |
| `2` | an unrecoverable media interval remains; its manifest is retained |

`go run` maps any non-zero child exit to `1` and prints `exit status N`; the
exact `2` is observed from the compiled binary that `scripts/selftest.sh`
builds.

Cases: `all normal audiofault truncated badindex corruptskip resume verifyfail
unrecoverable fatal`. `replay generate --out DIR` materialises a browsable
sample input tree (`events.json`, `params.json`, `video/*.fmp4`, `audio/*.rtp`).

## Layout

| Path | Responsibility |
| --- | --- |
| `media/clock` | exact rational clock, NS↔tick conversion with preserved remainders |
| `media/fault` | shared anomaly categories/severity/actions, fatal/recoverable errors, ledger |
| `media/mp4` | bounded box scanner, fMP4 sample/keyframe index, verified sidecar + rebuild |
| `media/rtp` | 2-byte length-framed parsing, 16→64-bit sequence extension, 80 ms reorder window |
| `media/audio` | exact Opus TOC and AAC AU-header packet durations |
| `media/segment` | event windows, keyframe placement, byte-exact fragment cuts, multi-layer fan-out and skew check |
| `media/recovery` | input+range+hash checkpoints, reuse/verify, targeted atomic rebuild |
| `media/audit` | SHA-256 helpers, sorted deterministic report/manifest model |
| `internal/fixture` | deterministic fMP4/RTP/event fixtures and sample-tree generator |
| `cmd/replay` | flag parsing, scenario orchestration, terminal counters, exit codes |
| `scripts/selftest.sh` | one-command local acceptance |

Each of the parsing, slicing and audit/recovery subsystems has its own
`_test.go` coverage; `cmd/replay/main_test.go` locks the acceptance counters
end to end.

## Processing notes

* No floating point enters a boundary decision: cross-timescale skew is an
  exact cross-product compared against the layer's one-frame duration.
* RTP duplicates are dropped before cutting and lost sequences are recorded
  but never synthesised.
* A checksum-failing sidecar is discarded; slicing uses only the index rebuilt
  from readable fragments.
* Cuts copy the init prefix plus whole `moof`/`mdat` byte ranges, so each
  produced `.fmp4` re-scans and re-indexes cleanly; there is no re-encoding.
* A corrupt start keyframe resynchronises to the next valid keyframe and the
  skipped interval is recorded as `range_uncovered`; a tail defect keeps the
  valid prefix. An event instant with no covered media is unrecoverable (`2`).
* Box and segment sizes are bounded by configurable `mp4.Limits` and
  `segment.Options.MaxSegmentBytes`; out-of-range reads are rejected.
