# media-check

A deterministic, local-only Go media processing kernel for multi-track timeline
validation, MP4 box/RTP scanning, damage localization, resumable jobs, and
byte-stable manifests.

## Run acceptance

```bash
go mod tidy
go build ./...
go test ./...
go run ./cmd/media-check demo --seed 17 --work /tmp/media-check
```

Expected normal output includes:

```text
tracks=3
frames=12
duplicate_groups=1
alignment_error_ms=0.000
frame_sha256=<64 hex chars>
segment_sha256=<64 hex chars>
pixel_min=0
pixel_max=255
pixel_mean=<number>
peak_dbfs=<negative number>
manifest_sha256=<64 hex chars>
```

The complete local acceptance script runs the normal case, all three corruption
modes, interrupt/resume, and Darwin arm64/amd64 cross-builds:

```bash
./scripts/acceptance.sh
```

## Corruption modes

```bash
go run ./cmd/media-check demo --seed 17 --corrupt truncated-box \
  --work /tmp/media-bad
go run ./cmd/media-check demo --seed 17 --corrupt missing-keyframe \
  --work /tmp/media-missing
go run ./cmd/media-check demo --seed 17 --corrupt timestamp-regression \
  --work /tmp/media-regression
```

Each damaged run reports `damage_count`, `first_bad_offset`, `track_id`, and a
unified millisecond `time_range`. The compiled CLI exits with code `2`.

> `go run` wraps a child process exit status: it prints `exit status 2` but the
> `go` command itself commonly exits `1`. To assert the kernel's exact exit
> code, build `./cmd/media-check` first or use `./scripts/acceptance.sh`.

## Interrupt and resume

```bash
go run ./cmd/media-check demo --seed 17 --work /tmp/media-recovery \
  --interrupt-after scan
go run ./cmd/media-check demo --seed 17 --work /tmp/media-recovery \
  --interrupt-after scan
go run ./cmd/media-check demo --seed 17 --work /tmp/media-recovery --resume
```

The second interrupt is an idempotent upsert and leaves one job record. Resume
loads `checkpoints/<task-key>.json`, verifies the input SHA-256, and continues
after the `scan` stage. If the input bytes changed, resume is rejected with
`checkpoint_fingerprint_mismatch` and exit code `4`.

The resumed manifest hash equals a clean run because manifests contain no
wall-clock timestamps, absolute work-directory paths, random values, or
process-specific resume flags.

## Layout and data flow

- `internal/clock`: integer ticks/timescale conversion, ranges, overlap.
- `internal/fault`: structured stage/code/track/time/offset errors.
- `internal/ingest`: bounded reads and per-task memory budget.
- `internal/fingerprint`: SHA-256 and stable task-key derivation.
- `internal/mp4`: MP4-compatible box parsing/building via `go-mp4`.
- `internal/fixture`: deterministic three-track, 12-frame synthetic asset and
  fault injection; no external media is used.
- `internal/stream`: sample bounds, `pion/rtp` parsing, timestamp and
  keyframe-interval checks.
- `internal/timeline`: track timescale, sample range, edit-point, transition,
  and overlap validation.
- `internal/diagnostic`: maps faults to merged timeline damage segments.
- `internal/export`: offscreen PPM/S16LE export, frame/segment hashes, pixel and
  audio statistics, and provenance.
- `internal/jobs`: atomic job records and stage checkpoints.
- `internal/manifest`: deterministic merge and atomic JSON archival.
- `internal/pipeline`: stage orchestration and resume.
- `cmd/media-check`: terminal acceptance entry point.

Generated work directory:

```text
<work>/
  input/media-001.mp4          # active input, possibly fault-injected
  evidence/media-001.valid.mp4 # unmodified generated baseline
  frames/frame-*.ppm           # offscreen exported frames
  audio/track-2.pcm            # exported audio bytes
  jobs/<task-key>.json
  checkpoints/<task-key>.json
  archive/<task-key>/manifest.json
  manifest.json
  manifest.json.sha256
```

## Time and error model

All media times are integer ticks plus an explicit positive timescale. The
common timeline scale is `1000` ticks per second. Conversion checks overflow and
range validity; out-of-bounds values produce structured faults rather than
silent clamping.

Faults retain:

- stage and stable error code;
- input ID and SHA-256;
- track ID;
- byte, sample, and box offsets where available;
- track-native time range and previous-sample evidence;
- causal message.

The same fault records are copied through scan results, diagnostic segments,
and the archived manifest.

## Duplicate material

`seg-2` and `seg-4` use the same `material_id` and deterministic frame pixels.
Their content fingerprint is therefore grouped once in
`fingerprint_groups`, while both `seg-2` and `seg-4` remain in
`timeline_refs`. Each timeline segment also has a distinct segment hash because
its ID and timeline position are part of the canonical hash input.

## Go/toolchain note

`go.mod` pins `github.com/abema/go-mp4 v1.4.1` and
`github.com/pion/rtp v1.10.6`. The pinned RTP module itself declares
`go 1.24.0`; with Homebrew `go@1.23` and the default `GOTOOLCHAIN=auto`, the Go
command transparently selects the required toolchain. Running `go mod tidy`
with Go 1.24 or newer may normalize the main `go` directive accordingly.
