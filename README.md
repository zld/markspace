# MediaRC — deterministic archive remux kernel

MediaRC is a local, dependency-pinned Go 1.23 toolkit for recovering aged
media archives. It generates its own synthetic MP4/MOV fixtures, demuxes
tracks and timed metadata, validates explicit track mappings, processes RTP
loss/reordering, advances a deterministic event-time timeline, and writes a
lossless synthetic ISO-BMFF container with verification reports.

There are no network services, media-external commands, or external fixture
files. All sample data is generated from the supplied seed.

## Layout

- `internal/model` — rational time bases, integer timestamps, structured media errors.
- `internal/demux` — go-mp4 box scanning/probing, MRCA archive manifest, and
  WebVTT/chapter/ID3/`emsg`/SEI parsers.
- `internal/track` — explicit source-to-target mapping and audio compatibility checks.
- `internal/timeline` — watermark advancement and deterministic late-event replay.
- `internal/rtp` — pion/rtp-backed sequence expansion, duplicates, reordering,
  wraparound, and bounded loss recovery.
- `internal/remux` — byte-preserving sample remuxing and single-value property checks.
- `internal/verify` — nearest-rank P95, A/V sync distance, hashes and comparisons.
- `internal/fixture` — deterministic normal, truncation, color-conflict and RTP cases.
- `internal/pipeline` — cross-subsystem orchestration and verification cases.
- `cmd/mediarc` — terminal adapter.
- `scripts/verify.sh` — build, test, demo and exit-code acceptance script.

## Build and test

```bash
go mod tidy
go build ./...
go test ./...
```

## Demo

```bash
go run ./cmd/mediarc demo \
  --seed 7 --loss 8 --late-ms 120 \
  --out /tmp/mediarc-out.mp4
```

The report includes:

- `av_sync_ms` (must be `<= 20`)
- `packet_recovery_rate` = recovered injected losses / injected losses (`>= 0.75`)
- `p95_latency_ms` using nearest-rank `ceil(0.95*N)` (`<= 500`)
- input and output sample counts
- per-track codec, sample count, time base, sample rate, channel layout,
  color summary and sample hash
- deterministic `output_sha256`

The CLI opens output with `O_CREATE|O_EXCL` and refuses an existing path.

An explicit mapping may be supplied, for example:

```bash
go run ./cmd/mediarc demo --out /tmp/mapped.mp4 --map 1:101,2:202
```

## Failure cases

Build the CLI to observe its exact process status:

```bash
go build -o /tmp/mediarc ./cmd/mediarc
/tmp/mediarc verify --case truncated          # exit 2, source_offset=...
/tmp/mediarc verify --case late-boundary      # exit 2, rewind_count=2
/tmp/mediarc verify --case color-conflict     # exit 2, conflict_fields=[...]
/tmp/mediarc verify --case loss-over-window   # exit 2, unrecoverable_packets=5
```

`scripts/verify.sh` runs all of these checks.

Note: the `go run` wrapper prints `exit status 2` for a child that exits `2`,
but the `go` command itself exposes its conventional wrapper status `1`. Use
the built binary or `scripts/verify.sh` when a CI system must observe status
`2` directly.

## Determinism

For fixed options, event ordering, timeline revision hashes, sample hashes and
the complete output SHA-256 are stable across repeated runs. Late events inside
the hard boundary trigger a full deterministic recomputation; events beyond the
boundary return `late_event_boundary` without mutating the accepted result.

## Scope

The encoded payloads are synthetic `synv`/`syna` samples rather than arbitrary
playable codecs. The kernel proves transpackage, timing and metadata preservation
for generated MP4/MOV structures; it does not promise bitstream conversion for
every historical codec. Implicit resampling and channel downmix are deliberately
unavailable.
