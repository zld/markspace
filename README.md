# mediacore

A deterministic Go media-processing kernel for real-time collaborative capture
streams. It ingests RTP packet streams and project-defined frame records, builds
a verifiable time index, and produces segments, silence intervals,
content-fingerprint duplicate groups and a self-checking manifest — all using a
single media clock and a single serializable error model.

No hardware codecs, devices, network I/O or browser dependencies. Every file it
writes goes into the output directory you name.

## Quick start

Requires Go 1.23 (tested on macOS arm64 and amd64).

```sh
go mod tidy
go build ./...
go test ./...

# Clean sample: 1200 frames, 12 segments, 2.4 s quiet, 3 duplicate groups.
go run ./cmd/mediacore demo --out /tmp/mediacore-demo

# Independent re-check of the material it produced.
go run ./cmd/mediacore verify \
  --input /tmp/mediacore-demo/frames.bin \
  --index /tmp/mediacore-demo/index.json \
  --manifest /tmp/mediacore-demo/manifest.json

# A burst-loss sample.
go run ./cmd/mediacore demo --case burst-loss --out /tmp/mediacore-loss
```

Exit codes: **0** verified, **1** damaged material or statistics mismatch,
**2** bad parameters or file operation failure.

## What the clean demo prints

```
frames=1200 segments=12 silence_ms=2400 duplicate_groups=3
target_kbps=900 actual_kbps=870 deviation_pct=3.33
alignment_error_ms=0
result=ok
```

`alignment_error_ms` is the disagreement between the media clock and sample
timing parsed back from a generated fragmented MP4 (via `Eyevinn/mp4ff`); the
acceptance bound is `<= 2 ms`.

## Demo cases

| case           | shows                                                              |
|----------------|--------------------------------------------------------------------|
| `clean`        | the acceptance summary above                                       |
| `burst-loss`   | explicit gaps, a gap-closed segment boundary, reduced bitrate      |
| `corrupt`      | checksum failure located by byte offset; repair then rebuild       |
| `truncated`    | truncated final record located; repair then rebuild                |
| `wrap`         | 32-bit RTP timestamp wraparound handled monotonically              |
| `digest-shift` | cached index reused, then rejected and rebuilt when input changes  |
| `packets`      | duplicate, reordered, late and burst-lost RTP packets              |

Damaged cases keep the un-repaired material in `<out>/damaged/` so you can
confirm that verifying it fails (exit 1) while the repaired rerun passes.

## Architecture

| package            | responsibility                                                     |
|--------------------|--------------------------------------------------------------------|
| `media`            | media clock (wrap, tolerance, comparison), error model, MFR1 frames |
| `ingest`           | RTP parsing, reorder/dedup window, gaps, bandwidth estimation      |
| `index`            | validate-and-scan, input/index digests, atomic versioned cache     |
| `pipeline`         | segmentation, silence detection, fingerprint dedup on one clock    |
| `manifest`         | self-recomputable manifest, MP4 timing binding, verification       |
| `internal/fixture` | deterministic material and fault injection                         |
| `internal/demo`    | scenario runner and reporting                                      |
| `internal/verify`  | independent artifact verifier                                      |
| `cmd/mediacore`    | terminal adapter                                                   |

### Frame record format (`MFR1`)

Big-endian, 96-byte header: magic, version, flags, header size, sequence,
RTP timestamp, capture time, dimensions, metadata length, payload length,
timing ticks, stream id, and a SHA-256 over the header prefix, payload and
metadata. The payload (`width*height` bytes) and JSON metadata follow. Truncated,
oversized, dimension-mismatched and checksum-failing records are reported with
their byte offset and never skipped silently.

### Determinism

The same input and parameters produce the same segment boundaries, fingerprint
set and manifest digest on every run. Re-running `demo` twice yields identical
`manifest.json` bytes.

### Performance

`BenchmarkScanSmallFrames` scans 64×64 records with ≤256-byte metadata at
roughly **200k records/sec** single-threaded (about 2× the 100k target) on an
Apple M1 Ultra; reorder and gap state are bounded by the configured window.
Run it with:

```sh
go test ./index/ -run '^$' -bench BenchmarkScan -benchtime 3x
```

## Limits and policy

- Frame payloads default to ≤64 MiB, metadata ≤1 MiB, dimensions ≤16384.
- Reorder window defaults to 64 packets; gap history is trimmed to a fixed bound.
- Silence is only confirmed from metadata agreement across a neighbor window;
  missing or corrupt metadata is reported as an *unknown* interval, never silence.
- Fingerprints group equal normalized content across timestamps; digest matches
  are confirmed by byte comparison so distinct frames are never merged.
- Index cache files are written to a sibling temp file, fsynced and renamed
  atomically; crash residue is discarded, never trusted.
