# medpipe

Local, dependency-light media pipeline for repairing timestamps and rebuilding
random-access indexes over H.264, H.265 and AV1 elementary streams delivered as
RTP captures. Everything runs on local files; there is no network access and no
external codec invocation.

## What it does

1. **Scan** — reads a length-prefixed RTP capture (`MPR1`), identifies H.264 /
   H.265 / AV1 frame boundaries, keyframes and in-band metadata, and reports a
   truncated trailing unit with its exact byte offset instead of indexing it.
2. **Normalize clocks** — maps every RTP timestamp onto a monotonic axis and a
   unified microsecond time base. Rollback, duplicate and missing timestamps are
   repaired when uniquely derivable (an explicitly configured nominal delta is
   required); an ambiguous gap stops the track. Original values, repaired values
   and corrections are all retained.
3. **Validate metadata** — audio sample rate / explicit channel map mismatches
   fail fatally; CICP color conflicts are recorded with source and adopted
   values (first-known-wins) and unknown values never override adopted metadata.
4. **Index** — builds a digest-bound random-access index (JSON sidecar plus an
   ISO-BMFF `tfra` box via go-mp4). The index is reused only when the input
   SHA-256 is unchanged; any content change invalidates and rebuilds it.
5. **Stream** — processes frames in bounded chunks with atomic checkpoints,
   writing one fMP4 fragment (`moof`/`tfdt`/`trun` + `mdat`) per chunk. An
   interrupted run resumes from the nearest confirmed keyframe.
6. **Manifest** — emits deterministic, wall-clock-free statistics on the unified
   time base and only marks the batch successful when no track is fatal.

## Layout

```
internal/
  errors.go          recoverable vs fatal error model (track + offset + advice)
  clock.go           unified clock, RTP wrap/rollback/duplicate/missing repair
  codec.go           H.264/H.265/AV1 keyframe and metadata classification
  capture.go         MPR1 length-prefixed capture writer
  scan.go            streaming scanner with bounded reassembly
  digest.go          SHA-256 content identity and stable run ids
  pathsafe.go        local-file checks, output sandbox, atomic writes
  index.go           digest-bound random-access index
  indexmp4.go        go-mp4 tfra and moof/tfdt/trun serialization
  audio.go           sample-rate and explicit channel-map validation
  color.go           CICP color conflict / unknown handling
  execute.go         chunked executor with checkpointed resume
  execute_reader.go  indexed random-access frame reader
  sink.go            fMP4 fragment sink
  manifest.go        deterministic manifest and aggregate metrics
  fixture*.go        self-generated normal/truncated/rollback/conflict/fatal set
  pipeline.go        scan → index → validate → stream orchestration
cmd/medpipe/         CLI entry point
```

## Requirements and dependency note

The brief targeted Go 1.23, but the pinned `github.com/pion/rtp v1.10.6`
itself declares `go 1.24.0` in its module file, so `go.mod` requires
`go 1.24.0`. The toolchain on this machine (Go 1.27) builds it cleanly; install
a >= 1.24 toolchain via `brew install go`.

Dependencies are pinned:

- `github.com/abema/go-mp4 v1.3.0` — `tfra` random-access and fragment boxes.
- `github.com/pion/rtp v1.10.6` — RTP packet marshal/unmarshal and timestamps.

## Acceptance flow

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/medpipe demo --generate --out /tmp/medpipe-fixture
go run ./cmd/medpipe run --input /tmp/medpipe-fixture --target-bitrate 4000000 --resume
```

The run prints `target_bps`, `actual_bps`, `bitrate_delta_pct`, `quality_mean`,
`quality_min`, `manifest_error_ms` and `index_rebuilt`. With the shipped
fixtures the bitrate delta is within 5% of 4 Mbps (calibrated to ~0.01%) and the
alignment error is 0 ms on well-formed tracks.

Re-running without changing the input reuses every index (`index_rebuilt: 0`)
and produces identical statistics. Mutating a capture changes its digest and
forces a rebuild.

## Generated fixtures

- `video_h264.rtp`, `video_h265.rtp`, `video_av1.rtp` — normal 30 fps streams.
- `audio_opus.rtp` — 48 kHz, 20 ms, explicit stereo channel map.
- `truncated_h264.rtp` — cut-off trailing record (recoverable, not indexed).
- `rollback_h265.rtp` — genuine backward timestamp jump, repaired to monotonic.
- `conflict_av1.rtp` — second-half CICP switch; conflicts counted, adopted value kept.
- `fatal/ambiguous_h264.rtp` — unrepairable duplicate; fails with track and byte
  offset and drives a non-zero process exit.

## Safety boundaries

- Inputs must be regular local files (no devices, pipes or symlinks).
- All output paths are confined to the run/working directory; parent traversal
  and symlink escapes are rejected.
- Index, manifest and checkpoint writes are atomic temp-file-and-rename.
- Per-track buffering is capped; the streaming stage holds only one chunk.
