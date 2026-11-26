# continuity — local HLS/DASH continuity engine

A self-contained Go kernel that ingests local HLS and DASH playlists plus their
CMAF fragments, demuxes audio/video without decoding, maps source tracks onto
stable output tracks, advances an event-time watermark, deterministically
recomputes the timeline when late data arrives, remuxes losslessly and can
checkpoint/resume. Everything runs offline from generated fixtures; there is no
transcoding, networking or system media dependency.

## Requirements

- Go 1.23 or newer
- macOS (Darwin) Terminal; no system media libraries
- Dependencies are pinned in `go.mod`:
  - `github.com/abema/go-mp4 v1.4.0` for MP4/CMAF box parsing and writing
  - `github.com/pion/rtp v1.8.11` for RTP header/sequence parsing

## Quick start

```sh
go mod download
go mod tidy
go build ./...
go test ./...
go run ./cmd/continuity demo --format hls,dash --late-ms 500 --queue-bytes 1048576
```

The demo generates two local sources (`main` and `backup`) from code, then
prints a per-sample provenance log and a summary. Expected normal-sample result:
`samples=8`, `time_delta_ticks=0`, `byte_delta=0`, `mapping_missing=0`, and the
same `sha256` for identical arguments on repeated runs.

### Fault scenarios

```sh
# Truncated box: non-zero exit, error names the fragment and byte offset.
go run ./cmd/continuity demo --fault truncated-box
# -> exit 2, failure_location=backup/video/seg-00001.m4s@<offset>

# Recovery: inside-window late sample recomputed once, out-of-window rejected.
go run ./cmd/continuity demo --fault late-with-checkpoint
# -> exit 0, late_recomputed=1, late_rejected=1, identical digest on repeat
```

Exit codes: `0` success, `1` usage error, `2` input corruption or boundary
rejection (including path escapes, mapping conflicts, queue-limit rejections and
checkpoint conflicts).

## Supported subsets

- **HLS**: media playlists only (master playlists are rejected), `EXTINF`,
  `EXT-X-MAP`, `EXT-X-BYTERANGE`, `EXT-X-TARGETDURATION`, `EXT-X-MEDIA-SEQUENCE`,
  `EXT-X-DISCONTINUITY(-SEQUENCE)`. Init and segment URIs are validated as
  relative and must stay inside the fixture root.
- **DASH**: static MPDs with a `SegmentTemplate` carrying a `SegmentTimeline`
  (`S@t`, `S@d`, `S@r`, including `r=-1` when the Period is bounded) plus
  `presentationTimeOffset`, `startNumber`, `$Number$`/`$Time$`/`$Bandwidth$`/
  `$RepresentationID$` placeholders and `BaseURL`. `SegmentList`, `SegmentBase`
  and duration-only templates are rejected rather than approximated.
- **Media**: fragmented MP4 / CMAF (`moof`+`mdat`) with `avc1`/`hvc1` video and
  `mp4a` (AAC) audio. Encrypted entries are rejected. Sample payloads, codec
  configuration and integer timing are preserved and re-demuxed after remux to
  prove the round trip is lossless.

## Time base

All timing uses integer `Ticks` at a single 90 kHz base (`BaseTimescale`).
Conversions are exact integer rescales rounded half away from zero; a mapping
that cannot be expressed exactly is rejected instead of rounded. 32-bit
timestamp wraparound is repaired by mapping each value into the window nearest
the previous unwrapped value.

## Modules

| Path | Responsibility |
| --- | --- |
| `internal/clock.go` | Integer time base, rescaling, decimal/ISO-8601 parsing, wraparound |
| `internal/fault.go` | Positioned error model and exit-code mapping |
| `internal/manifest.go` | HLS + source-manifest loading and reference validation |
| `internal/manifest_dash.go` | DASH MPD parser (SegmentTemplate/SegmentTimeline) |
| `internal/demux.go` | Fragmented MP4 demux, sample capture, track metadata |
| `internal/tracks.go` | Stable, lossless source→target track mapping |
| `internal/timeline.go` | Watermark, late recomputation, deterministic program |
| `internal/stream.go` | Bounded queue with backpressure and cancellation |
| `internal/mux.go` | Lossless CMAF remux and output HLS/DASH manifests |
| `internal/checkpoint.go` | Checkpoint save/restore with digest verification |
| `internal/fixtures.go` | Deterministic CMAF fixture builder |
| `internal/demo.go` | Demo corpus generation (parallel / switchover) |
| `internal/engine.go` | End-to-end pipeline and stable report |
| `cmd/continuity` | Terminal acceptance entry point |
