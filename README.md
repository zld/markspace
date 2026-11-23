# archivecheck

A reusable Go kernel for recovering long-term archived H.264 / H.265 / AV1
elementary streams and their keyframe indexes into auditable archive assets.
Everything is generated locally: no network, no external services, no device
access.

## Quick start

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/archivecheck demo --seed 23 --chunk-bytes 7 --queue 2 \
    --target-kbps 900,1800,3600 --consumer-delay-ms 15
```

The healthy demo exits `0` and prints, per layer, `codec`, `units`,
`keyframes`, `recoverable_gaps`, `target_kbps`, `actual_kbps`,
`bitrate_error_pct`, `parse_ratio`, `mean_keyframe_interval_ms`,
`max_keyframe_interval_ms`, `manifest_drift_ms` and `sha256`, plus the bounded
flow accounting.

Fault demonstrations:

```sh
go run ./cmd/archivecheck demo --case corrupt-index    # reports index_out_of_range
go run ./cmd/archivecheck demo --case boundary-skew     # reports boundary_conflict
```

Both exit `2`: damage was located and recovery completed. Exit `1` means a
parameter or internal error, including a failed invariant.

## Cases

| case | injected damage | expected evidence |
| --- | --- | --- |
| `ok` | none | no gaps, drift 0 |
| `split-startcode` | start code straddles chunk boundaries | `start_code_split` (informational), healthy |
| `corrupt-index` | keyframe index offset past end of stream | `index_out_of_range`, retained gap, resync |
| `obu-truncated` | AV1 OBU payload cut short | `obu_truncated`, unrecoverable gap |
| `keyframe-regress` | index keyframe time moves backwards | `index_time_regress`, retained gap |
| `boundary-skew` | mid layer timeline shifted 8 ms | `boundary_conflict` with per-layer evidence |

## Exit codes

| code | meaning |
| --- | --- |
| `0` | acceptance sample passed, asset complete |
| `2` | input damage located and recovery completed |
| `1` | bad parameters or internal error |

Note: `go run` maps a child exit code to its own status; build the binary and
run it directly to observe `2`.

## Layout

| path | role |
| --- | --- |
| `cmd/archivecheck` | terminal entry point and acceptance flags |
| `internal/media` | shared rational clock and structured fault model |
| `internal/stream` | bounded chunk reader, mux/demux, backpressured fan-out |
| `internal/codec` | Annex B NAL and AV1 OBU incremental parsers |
| `internal/index` | keyframe index parse/validate plus RTP event carriage |
| `internal/recovery` | per-layer segment recovery and shared-clock merge |
| `internal/mp4meta` | ISO-BMFF metadata document build and read-back |
| `internal/bitrate` | sliding-window target/actual rate measurement |
| `internal/manifest` | audit manifest, gaps, quality statistics, digest |
| `internal/verify` | pre/post-recovery invariants and deterministic digest |
| `internal/sample` | deterministic fixture generator for every case |

## Guarantees

- **Bounded memory.** The reader holds at most `queue+1` chunks (a token pool
  enforces it) and each fan-out consumer has a bounded queue. A slow consumer
  pauses the producer and never drops another layer's data. `flow
  peak_buffered_bytes` never exceeds `buffer_bound_bytes`.
- **No invented content.** Recovery only certifies an interval between two
  verified keyframe anchors. Truncation becomes a retained gap carrying the
  parser's fault kind and byte offset.
- **Unified time base.** Every layer is normalized through an exact rational
  clock onto one millisecond grid; boundary disagreements beyond tolerance are
  reported with each layer's evidence, never resolved by majority vote.
- **Determinism.** Fixtures use a fixed PRNG; repeated runs over identical
  input produce identical `sha256`, `verify_sha256` and manifest output.
