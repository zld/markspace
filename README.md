# media-audit

A reusable media-processing kernel for subtitle/clock/video reconciliation, and
a terminal adapter that proves its own results are reproducible.

Everything is generated locally: no network, no external services, no sample
media checked in. The only third-party dependencies are `go-mp4` (MP4 sample
and keyframe tables) and `pion/rtp` (RTP timestamp handling), both pinned.

## Requirements

- macOS with Go 1.23 or newer
- `go mod tidy` (or the explicit `go get` line below) to fetch dependencies

```sh
go get github.com/abema/go-mp4@v1.3.0 github.com/pion/rtp@v1.8.11
go mod tidy
```

## Run

```sh
go build ./...
go test ./...
go run ./cmd/media-audit -generate ./tmp/samples -out ./tmp/run -chunk 32 -resume
```

Expected output for the normal fixture set:

```
roundtrip_consistency=1.000
keyframe_index_hit_rate=1.000
segment_hash_match=1.000
```

### Scenarios

`-generate` writes three fixture sets under `<samples>` and a `fixtures.json`
descriptor for each. Select one with `MEDIA_AUDIT_SCENARIO`:

| Scenario | Contents | Expected |
| --- | --- | --- |
| `normal` | clean 25 fps clip, aligned SRT/WebVTT, steady RTP clock | exit 0, all ratios 1.000 |
| `edge` | DF-invalid frame labels, wrapped RTP timestamps, 2000 ppm drifting track, zero-length/reversed/overlapping and cut-spanning cues | exit 2, classified diagnostics, valid tracks still complete |
| `broken` | truncated sample plus an out-of-range keyframe index entry | exit 2, `index_fallback` with the damaged byte offset and `sample_read` for short reads |

```sh
MEDIA_AUDIT_SCENARIO=edge   go run ./cmd/media-audit -out ./tmp/edge   -samples ./tmp/samples -resume
MEDIA_AUDIT_SCENARIO=broken go run ./cmd/media-audit -out ./tmp/broken -samples ./tmp/samples -resume
```

### Resume

```sh
# stop after 2 committed chunks (exit 2, progress kept)
go run ./cmd/media-audit -generate ./tmp/samples -out ./tmp/run -chunk 32 -resume -interrupt-after 2
# finish the batch; committed chunks are verified, not redone
go run ./cmd/media-audit -generate ./tmp/samples -out ./tmp/run -chunk 32 -resume
# without -resume the batch restarts and must hash identically
go run ./cmd/media-audit -generate ./tmp/samples -out ./tmp/run -chunk 32
```

A resume reports `replayed_chunks=0`, never decreases `committed_chunks`, and
`segment_hash_match=1.000`. If any input or parameter changed, the run refuses
with `checkpoint_mismatch` (exit 1) instead of mixing old and new results.

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | success |
| 1 | bad arguments, unusable checkpoint, or a refused resume |
| 2 | one or more recoverable per-segment errors (and an intentional `-interrupt-after` stop) |

### Flags

| Flag | Default | Purpose |
| --- | --- | --- |
| `-out DIR` | required | checkpoint and report root |
| `-generate [DIR]` | off | build fixtures; `DIR` is accepted with or without `-samples` |
| `-samples DIR` | `<out>/samples` | fixture root |
| `-chunk N` | 32 | timeline items per committed chunk |
| `-resume` | off | continue instead of restarting |
| `-interrupt-after N` | 0 | stop after N chunks committed in this run |
| `-interval T` | 1/25 | frame sampling cadence, seconds (rational) |
| `-snap T` | 0 | snap subtitle boundaries onto video cuts within T seconds |
| `-max-drift-ppm N` | 500 | per-track drift threshold; negative disables |
| `-no-fallback` | off | disable the MP4 keyframe scan fallback |
| `-verify` | off | verify committed chunks without new work |
| `-quiet` | off | print summary only |

## Layout

```
cmd/media-audit/        terminal adapter
internal/model/         exact rational clock, tracks, classifiable errors
internal/timecode/      SMPTE parsing, RTP unwrap, drift estimation
internal/subtitle/      SRT/WebVTT parsing, overlap detection, cut splitting
internal/timeline/      merge, boundary snapping, deterministic ordering
internal/mp4/           sample tables, keyframe index, scan fallback
internal/render/        off-screen frame records and pixel hashes
internal/checkpoint/    atomic chunk store and local manifest
internal/pipeline/      batch orchestration
internal/report/        canonical digests and acceptance ratios
internal/fixtures/      deterministic MP4/SRT/VTT/timecode/RTP generators
```

All timestamps are exact `big.Rat` values, never floats, so identical inputs
and parameters produce byte-identical chunk digests.
