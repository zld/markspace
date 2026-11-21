# streamcheck

`streamcheck` is a local, terminal-only tool for game-release streaming build
engineers. It turns multi-bitrate media segments produced by interrupted or
resumed local transcoding jobs into deliverable HLS VOD playlists, then
re-reads those playlists and cross-checks them against the files and their
probed durations.

Everything runs locally. The only external dependency is FFmpeg 7.x's
`ffprobe` for reading media durations; the fixture generator does not need
FFmpeg at all.

## Requirements

- Go 1.22 or newer (built and tested with the toolchain in `go.mod`)
- macOS (Darwin arm64/amd64); any OS with `ffprobe` should work
- FFmpeg 7.x: `brew install ffmpeg`

## Quick start

```sh
brew install ffmpeg
go test ./...
go run ./cmd/streamcheck fixture --out /tmp/hls-fixture
go run ./cmd/streamcheck build --input /tmp/hls-fixture \
  --rates 800,1600 --segment-seconds 4 --tolerance-ms 80 --json
```

Or run the whole acceptance suite:

```sh
scripts/verify.sh
```

## Commands

### `streamcheck build`

```text
--input DIR              directory containing media segments (required)
--rates N[,N...]         requested bitrates in kbps (required, no duplicates)
--pattern NAME           naming rule (default "seg_{rate}_{index}.ts")
--segment-seconds SEC    nominal segment duration (default 4)
--tolerance-ms MS        max cross-rate boundary drift (default 80)
--strict                 treat missing segment ids as failures
--json                   emit a JSON report instead of text
--manifest-dir DIR       output directory (default: input dir, must be the
                         input dir or a direct child of it)
--ffprobe PATH           ffprobe executable (default "ffprobe")
--probe-timeout DUR      per-file ffprobe timeout (default 5s)
--concurrency N          parallel ffprobe processes (default 4, max 64)
```

The naming rule must contain `{index}`; `{rate}` is required when more than
one rate is requested. Both expand to unsigned decimal runs, so zero-padded
ids (`seg_800_007.ts`) are normalized numerically.

For each rate the tool:

1. scans the directory without following symlinks or subdirectories,
2. sorts segments numerically regardless of enumeration order,
3. probes each segment duration with a bounded, concurrent `ffprobe`,
4. reports missing ids, duplicate ids (with every conflicting path) and probe
   failures instead of fabricating entries,
5. writes `media_<rate>.m3u8` via a temporary file and atomic rename,
6. re-parses the written playlist and verifies ordering, durations, entry
   counts, `MEDIA-SEQUENCE` and that every entry exists on disk,
7. compares rates by segment count, id set and cumulative boundaries, and
8. reports SHA-256 digests of the playlists.

Playlists are never written for a rate with duplicate ids, an empty segment
set, or (in strict mode) missing ids. If any file changes size or mtime
between scanning and verification, the run fails with a change report.

### `streamcheck fixture`

```text
--out DIR                output directory (required)
--rates N[,N...]         bitrates to generate (default 800,1600)
--segments N             segments per bitrate (default 6)
--segment-seconds SEC    duration of each segment (default 4)
--pattern NAME           naming rule (default "seg_{rate}_{index}.ts")
```

Writes deterministic MPEG-TS segments (AAC with exact PCR/PTS timing, null
packets for bitrate-dependent sizes) plus `cases/missing`,
`cases/duplicate`, `cases/unordered`, and `cases/drift`. The generator is pure
Go: it never shells out to FFmpeg, and repeated runs produce byte-identical
files with fixed modification times.

## Output

JSON reports contain, per rate: `rate_kbps`, `segment_count`,
`discovered_file_count`, `total_duration_seconds`, `duration_delta_ms`,
`missing_ranges`, `duplicate_ids`, `validation_failures`, `manifest_path` and
`manifest_sha256`; plus top-level roll-ups, `warnings`, `failed` and
`exit_code`. Reports never contain file contents.

Example (happy path):

```json
{
  "rates": [
    {
      "rate_kbps": 800,
      "segment_count": 6,
      "total_duration_seconds": 24.012,
      "duration_delta_ms": 0,
      "missing_ranges": [],
      "duplicate_ids": [],
      "validation_failures": [],
      "manifest_path": "/tmp/hls-fixture/media_800.m3u8",
      "manifest_sha256": "..."
    }
  ],
  "failed": false,
  "exit_code": 0
}
```

## Exit codes

| Code | Meaning                                            |
|------|----------------------------------------------------|
| 0    | all checks passed                                  |
| 1    | media or manifest inconsistencies found            |
| 2    | parameter or runtime error (including ffprobe)     |

`scripts/verify.sh` uses the same contract and also verifies happy-path JSON
fields, anomaly exit codes, manifest structure, and rebuild reproducibility.

## Layout

```text
cmd/streamcheck/main.go          CLI entry point and orchestration
internal/config/flags.go         flag parsing and validation
internal/discover/files.go       directory scan and numeric normalization
internal/probe/ffprobe.go        bounded concurrent ffprobe durations
internal/manifest/model.go       playlist model and naming rules
internal/manifest/generate.go    M3U8 generation and strict re-parsing
internal/manifest/atomic.go      atomic manifest writes
internal/validate/consistency.go gaps, duplicates, drift, re-read checks
internal/report/report.go        text and JSON reports
internal/fixture/                deterministic MPEG-TS fixture generator
scripts/verify.sh                build + acceptance script
```

## Notes and limits

- Only local files are read; playlist entries must be bare names inside the
  input directory, and manifests are written only inside the input tree.
- Segments are probed by duration only; the tool does not decode media.
- The `--segment-seconds` value is metadata for reporting; durations always
  come from `ffprobe`, never from nominal targets.
