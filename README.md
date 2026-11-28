# archive-lab

Local HLS/DASH restoration lab. It parses a manifest, builds a SHA-256 index of
every input file, chooses a conversion configuration from the requested quality
goals, streams the segments once, and prints a JSON report of measured results.

No external services, no network access, no graphical dependencies.

## Requirements

- macOS (Darwin), Go 1.24 toolchain (the `pion/rtp` v1.10.6 module requires
  language version 1.24; a Go 1.23 install downloads it automatically through
  the module's toolchain directive when `GOTOOLCHAIN=auto`, which is the
  default).
- Dependencies are pinned in `go.mod`:

  | module | version | role |
  | --- | --- | --- |
  | `github.com/abema/go-mp4` | v1.4.1 | MP4/fMP4 box parsing and validation |
  | `github.com/pion/rtp` | v1.10.6 | RTP timestamp and sequence checks |

  Standard library covers HLS/DASH parsing, SHA-256, JSON and testing.

## Build and test

```sh
go mod tidy
go mod download
go build ./...
go test ./...
```

## Generate sample data

```sh
go run ./cmd/archive-lab fixture --dir .tmp/cases
```

This writes deterministic fixtures: normal segments, same-path replacement
bytes, metadata-only updates, a truncated segment, a malformed HLS playlist and
a malformed MPD. No external media files are needed.

## Run

```sh
go run ./cmd/archive-lab run \
  --input .tmp/cases/normal.m3u8 \
  --target-bps 800000 --max-bps 900000 \
  --min-psnr 35 --budget-ms 5000
```

The report includes `target_bps`, `actual_bps`, `bitrate_error_pct`, `psnr_db`,
`playlist_drift_ms`, `input_sha256`, `config_sha256`, per-segment progress,
cache state, index state and the list of unmet constraints.

Exit codes: `0` success, `1` usage error, `2` input or processing failure.

### Other commands and flags

```sh
archive-lab swap --from DIR --to DIR     # overwrite same-path files (replacement scenario)
archive-lab run ... --dry-run            # plan and report without streaming
archive-lab run ... --cancel-after N     # stop after N segments, then rerun to resume
archive-lab run ... --metadata FILE      # include a metadata file in the index
archive-lab run ... --index PATH --cache DIR --artifacts DIR
```

## Acceptance walk-through

Normal path (expect exit 0, rate error ≤ 5%, PSNR ≥ 35 dB, drift ≤ 1 ms):

```sh
go run ./cmd/archive-lab run --input .tmp/cases/normal.m3u8 \
  --target-bps 800000 --max-bps 900000 --min-psnr 35 --budget-ms 5000
```

Corrupt paths (expect exit 2, located error, no committed index):

```sh
go run ./cmd/archive-lab run --input .tmp/cases/truncated.m3u8 \
  --target-bps 800000 --max-bps 900000 --min-psnr 35 --budget-ms 5000
go run ./cmd/archive-lab run --input .tmp/cases/malformed.mpd \
  --target-bps 800000 --max-bps 900000 --min-psnr 35 --budget-ms 5000
```

Recovery path (same paths, changing bytes):

```sh
archive-lab run --input .tmp/cases/normal.m3u8 ...      # miss, index_rebuilt=true
archive-lab swap --from .tmp/cases/media/replace --to .tmp/cases/media/normal
archive-lab run --input .tmp/cases/normal.m3u8 ...      # miss, index_rebuilt=true
archive-lab swap --from .tmp/cases/media/metadata --to .tmp/cases/media/normal
archive-lab run --input .tmp/cases/normal.m3u8 ...      # miss, index_rebuilt=true
archive-lab run --input .tmp/cases/normal.m3u8 ...      # hit,  index_rebuilt=false
```

The last two reports carry the same `report_sha256`.

## Design notes

- **Integer time base.** All conversions go through `media.Timebase`
  (explicit timescale → integer nanoseconds, rounded half up). Boundary drift
  beyond 1 ms fails parsing instead of accumulating.
- **Located errors.** Every failure is a `media.Error` naming the stage, path,
  byte offset, manifest line or segment ordinal. No input is skipped silently.
- **Content-addressed index.** `scan` hashes the manifest, every segment, init
  segments and opted-in metadata. The index is written atomically and verified
  by a self digest; a corrupt or missing index falls back to a full scan. A
  file whose bytes change at the same path changes the digest, so the index is
  rebuilt.
- **Two-part cache key.** `sha256(input digest ‖ config digest)`. A config
  change misses without touching the index; an input change purges only the
  stale input's entries.
- **Measured, not invented, quality.** Segments are decoded only when they are
  uncompressed luma. For compressed codecs the report sets `psnr_db` to `null`
  and explains why, rather than printing a guessed number.
- **Path containment.** Every manifest reference is resolved and rejected if it
  escapes the input root, including through symlinks.
- **Bounds.** Per-file and total scan byte limits, a file-count limit, a luma
  extraction cap and a per-run segment sample cap are all enforced.

## One-shot acceptance

```sh
sh scripts/acceptance.sh
```

Builds the binary, generates fixtures, and checks the normal, damaged and
recovery paths, exiting non-zero on any failed expectation.
