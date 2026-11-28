# mediapipe — local multi-version media regression kernel

A terminal-only Go library plus one acceptance command for regression-checking
multi-bitrate VOD renditions. It maps several published versions of an asset
onto one exact common timeline, merges them, and verifies the result. Everything
is self-generated: no external media, files, or services are used.

## Requirements

- macOS (Darwin), Go 1.23 or newer
- No services, databases, or network access at run time

Pinned dependencies (in `go.mod`):

- `github.com/abema/go-mp4 v1.4.1` — MP4 box, track and edit-list parsing
- `github.com/pion/rtp v1.8.6` — RTP sequence/timestamp wire encoding

## Install dependencies

```sh
brew install go          # if Go is not already installed
go mod download github.com/abema/go-mp4@v1.4.1 github.com/pion/rtp@v1.8.6
go mod verify
```

`scripts/run.sh deps` performs the same steps.

## Build, test and run

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/regress demo --seed 17 --duration 600 --versions 3 --window 64
```

Or use the helper, which runs every step:

```sh
./scripts/run.sh all
```

The demo generates its own fixtures for the normal, reordered, duplicated,
truncated, bad-index, and slow-consumer cases.

## Demo flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--seed` | `17` | Fixture seed; identical seeds produce identical output |
| `--duration` | `600` | Seconds of generated media |
| `--versions` | `3` | Renditions per asset (minimum 2) |
| `--window` | `64` | RTP reorder window, in packets (minimum 8) |
| `--buffer` | `4` | Pipeline channel depth; the backpressure knob |
| `--memory` | `32` | Scanner memory budget in MiB |

## Output and exit codes

The demo prints:

```
av_sync_error_ms=8
loss_recovery_rate=1.0000
latency_p95_ms=17.704
missing=2
conflicts=4
unresolved_conflicts=0
recovered_indexes=2
packets=3525 duplicates=37
rollback_status=committed
manifest_digest=…
```

plus per-asset merge lines, the failure/rollback proof, the recovery re-scan
proof, and every damaged-fixture finding (asset, version, track, stage, byte
offset).

| Exit | Meaning |
| --- | --- |
| `0` | All checks passed; results were committed |
| `2` | Damaged input or unresolved conflict; output was rolled back |
| `1` | Internal error or invalid invocation |

Acceptance thresholds enforced by the demo and its tests: A/V sync error ≤ 40
ms, in-window loss recovery ≥ 95 %, and a P95 latency that is reproducible to
well within 10 % across runs.

## Packages

| Package | Responsibility |
| --- | --- |
| `internal/model` | Exact rational media time → nanoseconds (128-bit integer maths), raw timestamps retaining ticks/timescale/edit offset, spans, and one structured error type carrying asset/version/track/stage/offset with the 0/1/2 exit contract |
| `internal/manifest` | Reads and strictly validates the version manifest: asset → version → track → range, plus canonical JSON for stable hashing |
| `internal/mp4` | Bounded sequential box scanner (records every box offset, validates every length against the input end), sample index construction, and recovery of incomplete indexes from sample tables and media data. Metadata decoding uses `go-mp4`; the bounded walker owns all bounds checks so errors keep precise byte offsets |
| `internal/rtp` | Bounded RTP reorder window: in-window reordering, duplicate counting, explicit in-window vs beyond-window vs tail-loss accounting, and a hard rule that committed ranges are never rewritten |
| `internal/timeline` | Applies edit lists (trim, empty edits, rate) exactly and normalises every track onto a common version origin; reports measured A/V skew |
| `internal/merge` | Merges versions on atomic time intervals, itemises missing coverage per version, and reports summary conflicts. Content is compared by per-grid-cell digests, so different slicing of identical media is not a false conflict. Only an explicit, complete priority ranking selects a version, and the choice is recorded |
| `internal/pipeline` | Typed stages over bounded channels: slow consumers backpressure producers, the first failure stops the chain, and all stages observe one root cause |
| `internal/report` | Quantised, deterministic summary (integer µs, exact ratios), content and manifest digests, and a staging area that commits atomically or rolls back with a recorded reason |
| `internal/media` | Self-contained MP4 and RTP fixture generators, including the damage profiles and grid-cell content digests |
| `internal/demo` | The orchestration used by `cmd/regress demo`: builds fixtures, runs every subsystem, and asserts the acceptance criteria |
| `cmd/regress` | Terminal entry point |

## How the acceptance cases are produced

- **Healthy**: three renditions with different publication slice lengths, a
  non-zero raw media start hidden by the edit list, an empty edit, and one
  rendition with a removed chunk-offset table.
- **Reordered / duplicated / lossy**: every packet is marshalled by `pion/rtp`
  and parsed back before the window sees it. Losses within the window are
  re-sent late; two large runs are dropped beyond any window.
- **Truncated**: the file is cut mid-`mdat`, so a box claims bytes past the end
  of the input. The scanner reports the box offset.
- **Missing sample table**: `stsz` is zeroed, so recovery is impossible and the
  run must fail rather than invent samples.
- **Bad index**: `stco` is zeroed, so the index must be rebuilt. The rebuilt
  index is re-scanned and must hash identically to the clean file.
- **Slow consumer**: the pipeline contract test verifies backpressure and
  single-cause cancellation.
- **Failure/rollback**: a truncated rendition is staged then rejected; the
  identical run is repeated and must produce the same digest and rollback
  record.

## Boundaries

Only the fixtures this project generates are processed. The scanner enforces
configurable box, depth, table and sample limits and fails explicitly when a
region cannot be recovered; it does not claim to repair arbitrary damage.
