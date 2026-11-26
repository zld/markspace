# liveregress

A local, terminal-only audit kernel for live multi-bitrate releases. It parses
HLS manifests (master, media and I-frame playlists), demuxes MP4/fMP4, MPEG-TS
and RIFF/RF64 PCM WAV, maps tracks under explicit rules, copies media into new
fMP4 containers without re-encoding, rebuilds or checks keyframe indexes, and
leaves reproducible hashes behind.

Nothing is fetched from the network and nothing is re-encoded. Fixtures are
generated locally.

## Requirements

- macOS (Darwin) or any Go-supported host
- Go 1.23 (`brew install go@1.23`)
- Dependencies pinned in `go.mod`: `github.com/abema/go-mp4 v1.7.4`,
  `github.com/pion/rtp v1.9.0`

## Build and test

```sh
brew install go@1.23 && go mod tidy && go build ./... && go test ./...
```

## Generate fixtures

```sh
go run ./cmd/regress fixture --out /tmp/live-regression --case all
```

This writes one directory per case under `/tmp/live-regression`. Each case has
an HLS master with two bitrate variants, three segments per representation, and
a three-channel 24-bit WAV source plus an MPEG-TS/RTP rendition for every video
segment.

| case | meaning |
| --- | --- |
| `clean` | all segments intact |
| `truncated` | a video and an audio segment cut inside their mdat |
| `corrupt` | fragment metadata of one audio layer corrupted |
| `no-index` | I-frame playlists removed |
| `clock-gap` | one variant's declared duration disagrees with its media |
| `bad-mapping` | source WAV declares four channels for a three-channel map |
| `bad-path` | a playlist URI escapes the input root |

## Check a case

```sh
go run ./cmd/regress check --root /tmp/live-regression/clean --case clean
go run ./cmd/regress check --root /tmp/live-regression/truncated --case truncated --recover
go run ./cmd/regress check --root /tmp/live-regression/corrupt --case corrupt --recover
go run ./cmd/regress check --root /tmp/live-regression/no-index --case no-index --rebuild-index
go run ./cmd/regress check --root /tmp/live-regression/clock-gap --case clock-gap --recover
```

The clean case prints:

```
parse_roundtrip=1.000
keyframe_hit=6/6
segment_sha256_match=6/6
continuity_gap=0
exit_code=0
```

Every segment line carries the SHA-256 of its remuxed sample payload, the
container hash, and a per-segment payload-match flag. Audio layers also print a
`wav` line proving the remuxed PCM equals the source WAV frame by frame after
the explicit channel map; video layers print `ts` and `ts_remux` lines proving
the MPEG-TS rendition demultiplexes into byte-identical access units and that
those units survive a TS-to-fMP4 copy.

### Exit codes

The program returns 0 for a clean run, 2 when every defect is recoverable, and
1 for invalid mapping or an untrustworthy parse. **`go run` collapses any
non-zero exit status to 1**; the printed `exit_code=` line is authoritative.
Build the binary when exit codes matter to a shell:

```sh
go build -o /tmp/regress ./cmd/regress
/tmp/regress check --root /tmp/live-regression/truncated --case truncated --recover; echo $?  # 2
```

`scripts/acceptance.sh` does exactly that and asserts every case.

## What is checked

- **Manifests** — master, media and I-frame playlists; sequence numbers,
  durations, byte ranges, discontinuities and `EXT-X-MAP`.
- **Timing** — exact rational durations; a segment boundary is quantised once
  to integer ticks at a chosen timescale, never accumulated in floating point.
  RTP timestamps convert through the same clock.
- **Paths** — every playlist URI is resolved against the input root and any
  lexical, percent-encoded, or symlink escape is refused before reading.
- **Tracks** — every source track must be claimed by an explicit rule and every
  role produced once. Missing tracks, duplicate mappings and channel-count
  conflicts fail; there is no substitute track.
- **Lossless remux** — only container structure, timestamps and indexes change.
  PCM bytes and copyable codec samples are compared by digest before a segment
  is committed. Unsupported encodings fail explicitly.
- **Recovery** — a truncated tail keeps only the complete prefix and is
  recoverable; structural damage stops one representation while the others
  continue; checkpoints record only fully verified segments.
- **Indexes** — a missing I-frame index can be rebuilt by scanning sync markers;
  every declared index hit is checked against real keyframes.

## Layout

```
cmd/regress          CLI: fixture and check
internal/clock       exact rational media time, RTP conversion
internal/fault       pipeline-wide error (path, offset, track, time, recovery)
internal/manifest    secure path resolution and HLS parsing
internal/wav         RIFF/RF64 PCM16/24/32 reader with per-frame offsets
internal/ts          MPEG-TS packet, PSI, PES demux and keyframe scan
internal/fmp4        fMP4 init/fragment parsing and sample offsets
internal/track       explicit track and channel mapping
internal/remux       container-only rewrite with byte-verbatim copy
internal/index       keyframe index build, verification and rebuild
internal/verify      streaming SHA-256 over files and byte ranges
internal/recover     atomic, resumable checkpoints
internal/pipeline    run orchestration, isolation and reporting
internal/fixture     deterministic local media and case trees
scripts              acceptance driver
```
