# mediacheck

A reusable, deterministic Go media-processing kernel for lossy, reordered
RTP/RTCP video streams. It unifies media time and fault coordinates, scans
H.264/H.265 sample structure, localizes damage, manages content/container/
metadata identities, and performs recoverable manifest migration.

## Requirements

- Go 1.23 or newer
- Local terminal only; no network service is required at runtime
- Pinned dependencies:
  - `github.com/abema/go-mp4 v1.3.0`
  - `github.com/pion/rtp v1.8.11`

## Build and test

```sh
go mod tidy
go build ./...
go test ./...
go vet ./...
```

Run the generated end-to-end demonstration:

```sh
go run ./cmd/mediacheck demo --seed 7 --out /tmp/media-demo
```

`scripts/acceptance.sh` runs the full local verification sequence and the
demo twice, then compares the stable summaries for byte-identical output.

## Generated scenarios

The demo synthesizes all input locally:

1. `normal` — in-order, undamaged stream; expected loss and damage are zero.
2. `reorder_loss` — adjacent-packet reordering plus dropped packets.
3. `truncated_nal` — truncated RTP header and an H.264 FU whose final
   fragment never arrives; faults remain localized and later samples recover.
4. `same_path_replace` — same logical path with replaced media bytes; only
   the content cache is invalidated.
5. `metadata_update` — labels change only; content cache hits and the index
   is rebuilt.
6. `commit_interrupt` — migration fails during commit and restores the exact
   pre-migration manifest.

## Exit codes

| Code | Meaning |
| ---- | ------- |
| 0 | Success |
| 1 | Invalid arguments or internal error |
| 2 | Input was damaged, but the report is complete |
| 3 | Migration validation/commit failed and rollback succeeded |

The full six-scenario run is a self-check and exits 0. Use
`--scenario <name>` to observe a scenario-specific code.

> Note on `go run`: some Go tool versions (observed on Go 1.27) wrap a
> child process's non-zero exit into `go run`'s own exit code 1. The
> mediacheck binary itself returns the exact codes above; build it first
> (`go build -o mediacheck ./cmd/mediacheck`) when a specific 2/3 code
> must be observed. `scripts/acceptance.sh` does this automatically.

## Package layout

| Package | Responsibility |
| ------- | -------------- |
| `internal/clock` | RTP timestamp expansion, RTCP wall-clock anchor and trust flag |
| `internal/fault` | Shared packet, byte, time and fault-class model |
| `internal/rtp` | Bounded reorder, duplicate/seq-wrap handling and RTCP SR parsing |
| `internal/stream` | H.264/H.265 NAL assembly, samples, key frames and damage regions |
| `internal/pipeline` | End-to-end processing and three-way invalidation coordination |
| `internal/manifest` | Versioned manifest schema and v1-to-v2 adapter |
| `internal/cache` | Content-addressed payload cache identities and stats |
| `internal/index` | Independently invalidatable parse index |
| `internal/migrate` | Prepare/validate/commit migration with rollback points |
| `internal/fixture` | Seeded RTP/RTCP/MP4 test-data generators |
| `internal/mp4wrap` | Minimal go-mp4 reader/writer for generated samples |
| `internal/demo` | Scenario orchestration and deterministic terminal rendering |
| `cmd/mediacheck` | Terminal entry point |
| `test/acceptance` | End-to-end tolerances and stability assertions |

## Coordinates and determinism

- Sequence numbers are expanded across the 16-bit RTP wraparound.
- Timestamps are expanded across 32-bit wraparound and normalized to a
  zero-based per-track media clock.
- Every fault carries an expanded packet range, half-open byte range and
  media-time range.
- Stable summaries contain sha256 digests and counters only. They never
  contain runtime paths, wall-clock timestamps or map-iteration order.
- The same seed always produces byte-identical reports and summaries.

## Boundaries

This kernel analyzes packetization and container structure. It does not
decode video, reconstruct lost media content or promise recovery of missing
payload. Loss is reported, never fabricated. Large streams use a bounded
reorder window and explicitly report over-window gaps.
