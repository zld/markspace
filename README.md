# mediad — multi-camera delivery pipeline

A reusable Go library and terminal pipeline for turning long multi-camera MP4
recordings into verified, indexed, provenance-tracked deliveries after an event.
Every fixture is generated from a seed; nothing external is referenced.

## Quick start

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediad demo --seed 42 --target-bitrate 8000000 --resume
go run ./cmd/mediad demo --seed 42 --target-bitrate 8000000 --resume
go run ./cmd/mediad demo --seed 42 --corrupt truncated-table
go run ./cmd/mediad demo --seed 42 --corrupt edit-overrun
```

The clean demo prints one JSON object with the fields
`target_bps`, `actual_bps`, `bitrate_delta_pct`, `samples`, `keyframes`,
`timestamp_repairs`, `index_coverage_pct`, `quality_psnr_db`,
`alignment_error_ms`, `input_sha256`, `output_sha256` (plus `resumed`,
`seed`, and sparse-index fields). Two runs share an `output_sha256`; the second
reports `"resumed": true`.

## Exit codes

| code | meaning |
|------|---------|
| 0 | delivery verified |
| 1 | bad arguments or manifest (stage `args`/`parse`) |
| 2 | corrupt media or failed verification |

**Note on `go run`:** Go's `go run` driver reports any non-zero child exit as
`1` and prints `exit status N`; this is a Go toolchain limitation, not the
program's status. The program always returns 2 for corrupt media, and its JSON
error payload includes `"exit_code": 2` so the intended status is unambiguous.
To observe the numeric status directly, build the binary:
`go build -o mediad ./cmd/mediad && ./mediad demo --seed 42 --corrupt truncated-table`

## Architecture

```
cmd/mediad            terminal entry point (demo, verify)
internal/fixture      seeded MP4 + RTP + manifest generator, four corruption modes
internal/clock        nanosecond time base, edit-list mapping, structured errors
internal/manifest     strict camera-manifest parsing, path confinement
internal/mp4          bounded sample-table scan + timestamp repair
internal/index        keyframe seek index on the repaired timeline
internal/checkpoint   atomic, fingerprint-validated stage checkpoints
internal/provenance   deterministic delivery record and digests
internal/pipeline     orchestration, verification, resume planning
internal/metrics      bitrate, temporal quality, alignment statistics
```

### Time base

All times are integer nanoseconds. Ticks convert with one rounded 128-bit
division from the absolute value; per-sample deltas are never accumulated. The
cumulative error over three hours at 90 kHz stays below 1 ms (see
`clock.TestCumulativeRoundingUnderOneMillisecond`). The edit list's `elst`
media time is preserved, so a camera that starts early keeps a negative
pre-roll rather than having it trimmed away, and a camera that starts late
keeps its nonzero origin.

### Repair rules

- **DTS regression** — the local cadence (first positive delta) is continued and
  the exact source byte offset of the offending timing entry is recorded.
- **PTS before DTS** — composition time is clamped to decode time and recorded.
- **Gaps** — a hole beyond `MaxGapTicks` (20 s) is unrepairable and fails with
  the sample and byte offset. No sample is ever dropped.
- **RTP wrap** — 32-bit sender timestamps are extended across the wrap boundary;
  the wrap count is reported as `rtp_wrap_extensions`. Header encoding is
  round-tripped through `github.com/pion/rtp` so the extension math is exercised
  against real wire bytes.

### Verification

A run fails (exit 2) if, among other checks, the edit list claims more media
than exists, the repaired timeline is not strictly monotonic, index coverage is
below 99.5 %, or a derived-sync index is not disclosed. A failed verification
never writes a delivery report.

### Checkpoints and determinism

Each stage stores an input SHA-256, a parameter SHA-256 and a stage version.
A resume reuses an entry only when all three match; a corrupt or mismatched
entry invalidates it and every later stage. Checkpoints and provenance exclude
absolute paths and timing, so the same bytes in a different directory produce
the same `output_sha256`. The delivery digest also excludes run state
(`resumed`, resume notes), so cold and resumed runs hash identically.

### Quality metric

This pipeline does not decode video, so `quality_psnr_db` is a **temporal
fidelity** measure, not a picture-quality PSNR: it compares inter-sample
spacing on the repaired grid against the nominal cadence. A perfectly repaired
track yields the finite ceiling (99 dB); repaired or irregular spacing lowers
it. It is always finite.

## Corruptions

| `--corrupt` | what it does | traverses all stages? |
|-------------|--------------|-----------------------|
| `truncated-table` | stco count exceeds available bytes | no — scan reports the offset |
| `edit-overrun` | elst duration longer than the media | yes — fails verification |
| `rtp-wrap` | 32-bit sender clock crosses its boundary | yes |
| `missing-sync-flags` | no stss; keyframes inferred and labelled | yes |
| `sparse-keyframes` | > 10 s between keyframes | yes — reports max interval |
| `dts-regression` | tail samples share one DTS (capture-card jitter) | yes — repairs and records offsets |

Errors carry `stage`, `error_code`, `track_id` and `raw_offset`; `raw_offset`
points at the box header a reviewer can inspect with a hex dump.

## Safety

- Source paths resolve under the manifest root; traversal and absolute paths
  are rejected.
- Sample tables are bounded (`MaxSamplesPerTrack`, `MaxTracks`,
  `MaxEditEntries`), and every declared length is checked against the bytes
  actually present before parsing.
- Media payloads are never loaded whole; only sample tables and metadata are
  read, with seeking.
