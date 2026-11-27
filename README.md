# continuity

A reusable, fully local media-processing kernel for an image-frame sequence
wrapped in a minimal MP4 container. It is built around safe resumability: when
a primary/backup source switch replaces a file on the same path or updates its
container/metadata revision, stale content caches and frame indexes are
detected and rebuilt, and every output is independently re-verified before a
run is reported complete.

No capture devices, network services or external media are involved — all
input samples are generated deterministically in-process.

## Requirements

- macOS (Darwin), Go 1.23+ (developed and tested with the Go toolchain installed
  via the official package or Homebrew).
- Dependencies are pinned in `go.mod`:
  - `github.com/abema/go-mp4 v1.4.1` — MP4 box (`ftyp`/`mdat`) metadata.
  - `github.com/pion/rtp v1.8.11` — RTP timestamp and packet handling.

## Build and test

```sh
go mod tidy
go build ./...
go test ./...
```

## Demo

```sh
# Interrupt after frame 5, then resume from the last validated frame.
go run ./cmd/continuity demo --workspace /tmp/continuity-demo --frames 12 \
    --interrupt-after 5 --resume

# Repeating the same input/parameters yields identical hashes.
go run ./cmd/continuity demo --workspace /tmp/continuity-demo --frames 12 --resume

# Fault injection: a truncated container fails at the first locatable frame.
go run ./cmd/continuity demo --workspace /tmp/continuity-bad --inject truncate --frames 12

# Invalidation scenarios.
go run ./cmd/continuity demo --workspace /tmp/continuity-meta --mutate metadata  --frames 12
go run ./cmd/continuity demo --workspace /tmp/continuity-rep  --mutate replace   --frames 12
go run ./cmd/continuity demo --workspace /tmp/continuity-cont --mutate container --frames 12
```

The one-shot, asserted version of every scenario above is:

```sh
./scripts/acceptance.sh
```

### Exit codes

The program itself returns:

| Code | Meaning |
|------|---------|
| `0`  | success (including a deliberate `--interrupt-after` checkpoint) |
| `2`  | input or validation failure (a structured `error_stage/error_frame/error_offset` is printed) |
| `64` | argument/usage error |

> Note: `go run` wraps a non-zero child exit and reports `1` to the shell on
> this toolchain (it prints `exit status 2`). To observe the program's own
> `2`/`64` codes directly, build first (`go build -o continuity ./cmd/continuity`)
> and run the binary, or use `scripts/acceptance.sh`, which does this for you.

## Architecture and data flow

```
manifest ─▶ cache fingerprint ─▶ frame index ─▶ planner ─▶ worker pool ─▶ validator
   │              (content /          (RTP offsets   (candidate   (transform:     (re-reads
   │            container /            in mdat)       budget)     resample,        output)
   │            metadata)                                        channels, LUFS)
   └────────────────────── journal: per-frame state machine + atomic checkpoints
```

- `internal/clock` — rational media time base (90 kHz) with exact conversions
  and explicit overflow errors, shared by every module.
- `internal/model` — frame/output structs, SHA-256 helpers, structured errors
  (stage + frame + byte offset) and workspace-safe atomic file writes.
- `internal/fixture` — deterministic PCM frames, MP4 `ftyp`/`mdat` container,
  RTP packets and the `truncate`/`replace`/`container`/`metadata` mutations.
- `internal/manifest` — strict per-frame parsing; the first length, checksum or
  required-metadata error is reported and frames are never skipped.
- `internal/cache` — fingerprints over three independent axes: file **content**
  (mdat SHA-256), **container** revision and **metadata** revision.
- `internal/index` — locates each length-prefixed RTP packet inside `mdat` and
  rejects truncated trailing packets.
- `internal/planner` — enumerates sample-rate / layout / loudness candidates and
  excludes infeasible ones using thread count, output byte ceiling and loudness
  tolerance; with no feasible candidate the pool never starts.
- `internal/transform` — deterministic resampling, channel normalization and
  LUFS loudness control.
- `internal/pipeline` — concurrent workers on one clock, explicit failure states
  (duplicate sequence, zero-length frame, time overflow, corrupt data) and
  time/track-stable result ordering.
- `internal/journal` — the `discovered → planned → processing → processed →
  validated` lifecycle; `failed` is terminal. Checkpoints are atomic and resume
  continues after the last validated frame, redoing all unvalidated work.
- `internal/validate` — independently re-reads committed output and checks frame
  count, monotonic timestamps, geometry, loudness tolerance and hashes; one
  failure marks the whole run failed.
- `internal/engine` — the reusable core wiring the modules together; the `cmd/`
  entry point only generates fixtures and drives it.

## Cache and index invalidation semantics

| Change | Content cache | Frame index | Reported counters |
|--------|---------------|-------------|-------------------|
| same path, payload replaced | invalidated | rebuilt | `cache_invalidations=1` |
| container revision bumped (mdat unchanged) | retained | rebuilt | `index_invalidations=1` |
| metadata revision bumped | retained | invalidated | `index_invalidations=1` |

The content fingerprint hashes only the `mdat` payload, so an `ftyp`-only
container revision change leaves the content hash byte-identical while still
forcing the index to rebuild.

## Workspace safety

All paths are confined to the workspace via `model.SafeJoin` (absolute paths and
`..` traversal are rejected). Outputs are committed through temp-file-and-rename
(`model.AtomicWrite`); temp files use a distinct prefix and are never treated as
committed work by recovery logic.
