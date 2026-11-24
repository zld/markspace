# archive — media archive kernel

A reusable Go kernel for archive and restoration work: parse MXF and MP4 track
metadata, resolve conflicting rotation/crop/colour declarations, compose a
multi-track nanosecond timeline, recover RTP representation layers over a lossy
link, fan them into a shared clock, and emit deterministic PNG frames plus a
frame manifest and JSON statistics.

Everything is local and self-contained. Sample material is generated in-process;
no external files or services are used.

## Quick start

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/archive demo --generate --out /tmp/archive-demo \
  --loss-burst 3 --bandwidth-kbps 900,420,1100
```

The demo exits 0 when recovery succeeds, prints a JSON summary on stdout, and
writes:

- `assets/` — normal, boundary, invalid-metadata and truncated samples
- `frames/` — deterministic PNG evidence
- `manifest.json` — frame list, hashes, layer outcomes
- `summary.json` — the same JSON summary printed to stdout

To inspect one container directly:

```sh
go run ./cmd/archive probe /tmp/archive-demo/assets/normal.mp4
```

### Exit codes

| code | meaning |
|------|---------|
| 0 | success (recovery produced at least one usable layer) |
| 2 | command-line usage error |
| 3 | truncated container |
| 4 | boundary/alignment fault or isolatable timeline error |
| 5 | invalid or unsupported metadata |

## Layout

| package | role |
|---------|------|
| `internal/clock` | shared nanosecond timebase, modular wrap expansion, classified errors |
| `internal/fixture` | deterministic in-process MP4/MXF/RTP writers |
| `internal/probe` | MP4 (`go-mp4`) and MXF KLV metadata parsing |
| `internal/normalize` | fixed-priority resolution of rotation/crop/colour conflicts |
| `internal/timeline` | multi-track composition onto one timeline |
| `internal/rtp` | dedup/reorder/loss recovery, bandwidth scheduling |
| `internal/segment` | shared-clock fan-in and layer isolation |
| `internal/render` | offscreen frame synthesis, transform, PNG, PSNR |
| `internal/manifest` | frame manifest, hashes and statistics |
| `internal/pipeline` | demo orchestration |
| `cmd/archive` | terminal entrypoint |

## Resolution rules

1. A crop covering the whole stored frame is the default; any legal explicit
   crop replaces it. Invalid crops (negative origin, past the stored frame, or
   empty) are classified errors naming the track and produce no frames.
2. Rotation follows the track's explicit declaration. A non-right-angle matrix
   is unsupported rather than guessed.
3. Two or more distinct colour declarations are marked `unresolved`, every
   source is recorded, and the first is retained only for provenance.

## Transport rules

- Duplicate sequence numbers count once.
- Packets are reordered within a bounded window; a packet arriving after its
  region is confirmed is counted as late and marked, never applied.
- Missing runs are retained as recoverable gap markers.
- Fan-in checks each layer's start, end and every frame boundary against the
  shared clock. A layer beyond tolerance is isolated with a reason; survivors
  keep producing.

## Determinism

Reference frames derive from a per-track seed, transforms are exact integer
crop/rotate operations, and PNG encoding emits no timestamps. Re-running the
demo produces byte-identical frames, `manifest.json` and hashes.
