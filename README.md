# mediascan

A reusable Go media-processing kernel for remote production teams that
receive HLS/DASH playlists and fragmented MP4 (fMP4) segments. It analyzes
bitstream structure and keyframe spacing, localizes corrupted regions to
their original byte offsets, normalizes every clock onto one rational time
base, and emits reproducible hashes and quantitative summaries.

The scope is deliberately limited to **recognizable media structures inside
manifests and segments**. It does not transcode and never fabricates missing
content.

## Architecture

```
manifest + segment fixtures
        │
        ▼
internal/manifest   HLS (.m3u8) and DASH (MPD) parsing; segment order,
                    duration, media sequence, time range, gaps, duplicates,
                    discontinuities (each with a source-document offset)
        │
        ▼
internal/stream     fMP4 box walk (type/length/track time base/sample
                    ranges, truncation boundary + verified prefix) and RTP
                    packet parsing via pion/rtp
        │
        ▼
internal/clock      rational time base, 32-bit wrap expansion, negative-jump
                    rejection, multi-point least-squares drift estimation
        │
        ▼
internal/pipeline   parse → transform (time normalization) → validate
                    (re-parse + go-mp4 cross-check + monotonicity), with
                    damage isolation and checkpoint/resume
        │
        ▼
internal/index      VFR-aware keyframe index (segment + sample + byte offset)
internal/report     per-segment dual SHA-256 (input vs processed) and summary
internal/fault      stage/segment/byte-offset anchored fault records
internal/fixture    deterministic in-repo data generation (no external files)
        │
        ▼
cmd/mediascan       terminal acceptance driver
```

## Build and test

Requires Go 1.23+ (the module's `go.mod` pins the language version; third
party dependencies are pinned to `go-mp4 v1.3.0` and `pion/rtp v1.8.9`).

```sh
go mod tidy
go build ./...
go test ./...
```

## Acceptance runs

A helper script reproduces the full acceptance matrix and prints the real
process exit codes:

```sh
./scripts/acceptance.sh
```

Equivalent manual commands (run the compiled binary, not `go run`, so the
documented process exit codes are preserved — `go run` maps a child exit
code of 2 to its own 1):

```sh
go build -o mediascan ./cmd/mediascan

./mediascan --generate --case normal  --format hls,dash
./mediascan --generate --case damaged --truncate-box 17 \
            --timestamp-wrap --vfr --drift-ppm 120
./mediascan --generate --case boundary \
            --duplicate-sequence --missing-segment
./mediascan --generate --case damaged --resume
```

### What each case proves

- **normal** — `roundtrip_consistency=1.000`, `keyframe_hit_rate=1.000`,
  one input and one output SHA-256 per segment, `damage_count=0`, exit `0`.
- **damaged** — prints the truncated box's original byte offset, the
  isolated byte range, the verified prefix, and the count of later segments
  still safely processed; RTP wrap events and the drift fit are shown;
  exit `2`.
- **boundary** — both HLS and DASH report the duplicate sequence id and the
  missing segment with their source-document offsets; exit `2`.
- **resume** — writes a checkpoint under the OS temp directory, re-runs, and
  confirms the index and all hashes match the first run; exit `2` for the
  underlying damaged material, `resume_verification=ok`.

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | Clean run, all round trips and hashes consistent |
| 1 | Bad arguments or internal error |
| 2 | Media damage (truncated/oversized box, missing segment, hash/round-trip mismatch) or manifest boundary error |

All rates are bounded to `[0,1]`. Reports use segment names and offsets only
— never absolute host paths — so they are safe to exchange.

## Design notes

- **VFR, not assumed CFR.** Keyframe intervals and frame rates derive from
  actual sample timestamps; a fixed frame rate is never substituted.
- **Wrap vs. backward jump.** A modular forward delta inside half a cycle is
  a rollover and is expanded; a delta beyond half a cycle is a genuine
  negative jump, recorded as a fault rather than treated as a new segment.
- **Damage containment.** A truncated box keeps its start offset as the
  unsafe boundary; the verified prefix is retained, the segment's dependent
  index entries are quarantined, and independent later segments continue.
- **Determinism.** Normalized segments are canonical JSON; identical input
  always yields identical indexes, fault offsets and hashes, which the
  checkpoint/resume path verifies explicitly.
