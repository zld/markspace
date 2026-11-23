# mediapack

A reusable Go media-pipeline library for multi-track MXF/MP4 ingest. It scans
container metadata, aligns drifting/wrapping/VFR timestamp streams onto one
integer-rational master timeline, cuts common-decodable-point segment lists,
aligns a multi-bitrate ladder, and streams deterministic outputs with
resumable checkpoints. It does **not** decode, transcode or render video.

## Architecture

Data flow:

```
scanner (probe) -> index -> timeline -> manifest + ladder -> runner (stream/checkpoint)
```

| Package | Responsibility |
|---|---|
| `internal/model` | Exact integer-rational time base; structured errors with track + byte range + stage |
| `internal/probe` | MP4 box tree (go-mp4) and MXF KLV scans; rotation/crop/color kept raw and normalized with priority + source |
| `internal/index` | Content-addressed persistent index; any input-summary or scan-version change invalidates and rescans |
| `internal/timeline` | 32-bit RTP unwrap, wrap/duplicate/regression classification, drift estimation (ppm), VFR common decodable points |
| `internal/manifest` | Segment cuts on common decodable points with realized deviation and tolerance rejection |
| `internal/ladder` | Intersection of rendition decodable points; unsatisfiable renditions rejected |
| `internal/runner` | Bounded-memory streaming, temp-file + verify + atomic replace, validated checkpoints with safe-boundary rollback |
| `internal/fixture` | Self-generated drift/wrap/VFR inputs and byte-offset fault injectors (no external files) |
| `cmd/mediapack` | Terminal-only acceptance/demo adapter |

All time math is integer rational (`internal/model/time.go`); floating point
is never used to accumulate media time. Every offset/length/time multiply is
overflow-checked.

## Determinism and safety

- Repeated runs with identical inputs/parameters produce byte-identical
  manifests and output checksums (content excludes absolute work paths).
- Outputs and checkpoints are written to temp files, checksum-verified, then
  atomically renamed. A failed run never leaves a seemingly complete manifest.
- A checkpoint is trusted only after its own checksum and every completed
  segment digest verify; otherwise execution rolls back to the last safe
  boundary.
- Multiple errors are sorted by earliest input offset.

## Acceptance

```sh
go mod tidy && go build ./... && go test ./... && \
go run ./cmd/mediapack demo --work ./tmp/demo --tracks 3 --ladders 3 \
    --segment 2s --faults all --resume
```

Printed keys include `tracks`, `aligned_ladders`, `fault_offset_bytes`,
`fault_error_bytes<=16`, `resume_ms<=500`, `checkpoint_reused`,
`manifest_sha256` and `output_sha256`. Two independent fresh runs emit the same
two SHA256 values.

Exit codes: `0` success, `2` corrupt input / unalignable, `64` bad arguments.

The `check` subcommand exercises the data-fault and unalignable exit-2 paths:

```sh
go run ./cmd/mediapack check --work ./tmp/c --truncate-box
go run ./cmd/mediapack check --work ./tmp/c --truncate-mxf
go run ./cmd/mediapack check --work ./tmp/c --unalignable
```

## Limits

Configurable caps live in `probe.DefaultLimits` and `runner.Config`
(max input bytes, tracks, boxes, box depth, segments, segment/output bytes).
