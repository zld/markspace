# hlscheck

A local, dependency-light HLS slicer and consistency validator for multi-bitrate
MPEG-2 transport streams. It generates deterministic synthetic H.264 TS
fixtures with FFmpeg, slices them into keyframe-aligned HLS segments entirely
in Go, and validates the resulting playlists and segments — including
deterministic fault injection for testing recovery behavior.

Everything runs locally and on the terminal. There is no network access, no
GUI, and FFmpeg is used only to synthesize source streams (never for HLS
slicing).

## Requirements

- macOS (Apple Silicon or Intel)
- [mise](https://mise.jdx.dev/) and Go 1.22.12
- FFmpeg 7.1 or newer

```sh
brew install mise ffmpeg
mise install go@1.22.12
```

## Layout

```
cmd/hlscheck/main.go        CLI parsing, commands and exit codes
internal/model/stream.go    bitrate, timestamp and segment records
internal/ts/reader.go       188-byte packet reader (residual packets, offsets)
internal/ts/psi.go          PAT/PMT section reassembly and parsing
internal/ts/pes.go          PES timestamps and Annex-B IDR detection
internal/ts/clock.go        33-bit PTS unwrap, per-PID continuity tracking
internal/ts/demux.go        streaming video-frame demuxer
internal/slicer/            bounded-memory keyframe analysis and segment writer
internal/hls/manifest.go    canonical master/media playlist + index writer
internal/validate/          file, playlist and cross-rendition checks, faults
internal/fixture/generate.go FFmpeg-backed synthetic stream generator
internal/fsutil/safe.go     root-confined paths and atomic file replacement
scripts/demo.sh             reproducible end-to-end demonstration
```

## Build and test

```sh
mise exec go@1.22.12 -- go test ./...
mise exec go@1.22.12 -- go build -o /tmp/hlscheck ./cmd/hlscheck
```

## Commands

### demo — generate, slice and validate

```sh
work="$(mktemp -d)"
/tmp/hlscheck demo --work "$work" --duration 11s --rates 350000,800000 --target 2s
```

Per-rendition text metrics (also available as JSON with `--json`):

```
rendition_bps=350000 segment_count=6 duration_ms=11000 deviation_ms=0 failure_count=0
rendition_bps=800000 segment_count=6 duration_ms=11000 deviation_ms=0 failure_count=0
result=OK
```

### fault — deterministic corruption

```sh
/tmp/hlscheck fault --root "$work/hls" --drop 800000:2 --swap 350000:1:3
```

- `--drop BPS:SEQ` deletes the named segment file.
- `--swap BPS:A:B` exchanges two playlist URI positions.

### validate — full consistency check

```sh
/tmp/hlscheck validate --root "$work/hls" --index "$work/hls/index.json"
```

Findings use stable codes with the rendition bitrate and segment sequence:

```
FINDING code=MISSING_SEGMENT bps=800000 seq=2 detail="..."
FINDING code=ORDER_MISMATCH bps=350000 seq=1 detail="..."
```

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | success |
| 1 | argument or processing error (including missing/old FFmpeg) |
| 2 | media consistency failure |

## Failure codes

| Code | Meaning |
|------|---------|
| `MISSING_SEGMENT` | a referenced segment file does not exist |
| `DUPLICATE_SEGMENT` | one file is referenced by more than one sequence |
| `ORDER_MISMATCH` | playlist URI order disagrees with the index sequence |
| `DANGLING_REFERENCE` | playlist references a file absent from the index |
| `DURATION_DEVIATION` | an interior segment exceeds the one-frame duration limit |
| `BOUNDARY_MISALIGNED` | cross-rendition boundary differs beyond one frame |
| `SEGMENT_COUNT_MISMATCH` | renditions do not share a segment count |
| `PLAYLIST_DRIFT` | on-disk playlist differs from the canonical regeneration |
| `CONTINUITY_ERROR` | TS continuity counter duplicate or undeclared jump |
| `INDEX_UNREADABLE` | index.json cannot be read or parsed |

## Design notes

- **Bounded memory.** Analysis decides cuts in a single pass and retains only
  chosen boundaries; the packet reader holds at most one residual 188-byte
  packet; continuity errors keep a capped sample plus an occurrence total.
  Memory scales with segment count, not total stream duration.
- **Keyframe guarantee.** Segments start only on IDR frames. If no IDR falls
  within twice the target duration, the run fails with
  `ErrNoKeyframeInWindow` rather than emitting a non-keyframe segment.
- **Atomic publishing.** Every segment and playlist is written to a temp file
  and renamed into place, so interrupted writes never leave partial outputs.
- **Path containment.** All writes stay under the supplied root; traversal
  components and absolute paths are rejected.
- **Canonical playlists.** Playlist serialization is deterministic, so the
  validator regenerates the expected playlist from `index.json` and diffs it
  byte-for-byte.
