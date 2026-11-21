# hlsflow

A dependency-free local tool that packages unreliable multi-rendition
H.264/AAC MPEG-TS input into a verified HLS publishing directory. All TS
parsing, M3U8 generation/parsing and SHA-256 handling use the Go standard
library; no third-party runtime dependencies and no transcoding.

## Build and test

```sh
brew install go
go test ./...
go build ./...
```

Requires Go 1.22+ (the module declares `go 1.22`).

## Commands

```sh
# 1. Generate a deterministic sample tree (clean or with injected faults).
go run ./cmd/hlsflow fixture --dir .tmp/demo --scenario faults \
    --rates 800000,1800000,3500000

# 2. Build the HLS tree. Source segments are reordered by filename sequence,
#    gaps wait up to --gap-timeout, cuts happen only on H.264 IDR boundaries,
#    and missing source data produces EXT-X-DISCONTINUITY rather than a splice.
go run ./cmd/hlsflow build --job .tmp/demo/job.json --input .tmp/demo/inbox \
    --output .tmp/demo/out --target-duration 2s --gap-timeout 3s --finalize

# 3. Re-parse the published playlists from disk and verify existence,
#    SHA-256, EXTINF timing and cross-rendition drift.
go run ./cmd/hlsflow verify --job .tmp/demo/job.json \
    --master .tmp/demo/out/master.m3u8 --json
```

## Output

One line per rendition, e.g.

```
rendition=720p bitrate=1800000 segments=11 duration=21.98s drift=+0.06s \
  missing=1 reordered=2 checksum_failures=1
```

`verify --json` additionally lists each failing segment number and reason.

## Exit codes

| Code | Meaning                                                    |
|------|------------------------------------------------------------|
| 0    | No consistency problems                                    |
| 1    | Playlist/segment verification failure (checksum, timing…)  |
| 2    | Bad arguments, configuration or unparseable input          |

## Package layout

- `internal/config` — job loading/validation (duplicate, range, codec, path)
- `internal/ts` — 188-byte packets, continuity counters, PAT/PMT, PTS/PCR
  wrap handling, Annex B NAL/IDR detection
- `internal/slicer` — sequence reorder, gap wait, IDR-boundary cutter
- `internal/m3u8` — media/master playlist generation and re-parsing
- `internal/integrity` — SHA-256 index and in-memory fault injection
- `internal/safeio` — root-confined paths and atomic temp-then-rename writes
- `internal/pipeline` — build orchestration, incremental publish, verify
- `internal/fixture` — deterministic TS generator and fault scenarios

## Safety properties

- Playlists and the integrity index are written to a temp file in the
  destination directory and atomically renamed, so an interrupted process
  never leaves a half-written manifest.
- Every path is confined to the configured input/output root; traversal such
  as `../` is rejected.
- Checksum faults are applied to the bytes read for hashing only; files on
  disk are never mutated.
