# mediacheck — offline Matroska/WebM manifest kernel

A reusable, fully local media-processing kernel for broadcast operations. It
parses multi-bitrate Matroska/WebM masters, normalises every track onto one
nanosecond timebase, aligns representation switches onto shared keyframe
boundaries, emits deterministic segment manifests, and persists checksummed
checkpoints that let an interrupted run resume without changing the result.

No network access, no external services, no transcoding, no payload decoding.
All sample material is synthesised in Go.

## Architecture

| Subsystem | Package | Responsibility |
| --- | --- | --- |
| Parse & timeline | `internal/ebml`, `internal/matroska`, `internal/timeline` | bounded EBML reader, block/track rebuild, wrap expansion, drift fit |
| Align & manifest | `internal/align`, `internal/manifest` | cross-representation keyframe alignment, deterministic manifest |
| Recovery & index | `internal/index` | checksummed chunk checkpoints, rollback, resume cursor |
| Orchestration | `internal/pipeline`, `cmd/mediacheck` | end-to-end run, invariants, terminal UI and exit codes |

Data flow: EBML parse → interleaved track blocks → unified nanosecond timeline
→ representation alignment → deterministic manifest + write-ahead index →
invariant check before commit.

## Build and verify

```sh
go mod tidy
go build ./...
go test ./...
# or the end-to-end driver (also asserts exit codes):
./scripts/acceptance.sh
```

## Demo

```sh
go run ./cmd/mediacheck demo --seed 7 --rates 3 --segments 12 --checkpoint-every 4
go run ./cmd/mediacheck demo --corrupt cluster-truncate
go run ./cmd/mediacheck demo --interrupt-after 8 --workdir ./.mc
go run ./cmd/mediacheck demo --interrupt-after 8 --resume --workdir ./.mc
```

For fault cases the real exit codes (2 and 3) are visible when running a built
binary; `go run` collapses non-zero exits to 1.

## Failure model and exit codes

* `0` — run completed and all invariants passed.
* `2` — corrupt material or timeline ambiguity (bad VINT, truncation, unknown
  track, ambiguous wrap, drift over threshold, unreconcilable keyframes). The
  reported offset is the first illegal byte (±0 for truncation).
* `3` — index verification or recovery failure (checksum mismatch, foreign key,
  cursor regression, simulated interrupt).

The five synthetic fixture classes are `unknown-nesting`, `cluster-truncate`,
`missing-track`, `wrap-drift` and `vfr-misalign`. At least three (truncation,
missing track, wrap/drift ambiguity) propagate into alignment/recovery and
block manifest commit.

## Determinism and resource bounds

Identical input bytes and configuration produce byte-identical manifests and
the same output SHA-256. The EBML reader streams payloads through
`io.CopyN(io.Discard, …)` and buffers only VINT headers; the 8 GiB file ceiling
and 32-level nested-depth limit live in `ebml.DefaultLimits`.
