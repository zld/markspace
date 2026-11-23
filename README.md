# mediaforge / mediacheck

A local, deterministic media-processing kernel for broadcast RTP/RTCP audio.
It parses imperfect captures, maps RTP clocks onto one rational media time,
replays late packets, normalizes sample rate and channel layout, controls
loudness, remuxes to MP4 with a rebuilt index, and reports verifiable quality
statistics.

## Requirements

- Go 1.23 (macOS/Darwin)
- Local files only; no network services
- Dependencies: `github.com/pion/rtp v1.8.11`, `github.com/pion/rtcp v1.2.15`,
  `github.com/abema/go-mp4 v1.4.1`

## Install and build

```sh
brew install go
go mod tidy
go build ./...
go test ./...
```

## Demo

```sh
go run ./cmd/mediacheck generate --out /tmp/media-fixtures
go run ./cmd/mediacheck run --input /tmp/media-fixtures/normal.rtp \
  --sample-rate 48000 --channels 2 --target-lufs -23 --out /tmp/media-out
go run ./cmd/mediacheck run --input /tmp/media-fixtures/damaged.rtp \
  --sample-rate 48000 --channels 2 --target-lufs -23 --out /tmp/media-damaged
go run ./cmd/mediacheck verify --dir /tmp/media-out
```

Exit codes: `0` processed and verified, `1` input or verification failure,
`2` parameter error.

## Generated fixtures

| File | Contents |
| --- | --- |
| `normal.rtp` | 250 in-order packets, valid payload CRC |
| `reordered.rtp` | same audio with swaps and three duplicates |
| `damaged.rtp` | six-packet hole, bad-version packet, CRC-corrupt payload, truncated packet and trailing fragment |
| `wrap.rtp` | timestamps that wrap past 2^32 with three late arrivals |

## Outputs (`<stem>.{mp4,manifest.json,stats.json}`)

`run` prints and stores the contractual fields: `packets`, `loss`,
`duplicates`, `late_replays`, `bad_packets`, `input_bitrate_bps`,
`target_bitrate_bps`, `bitrate_error_pct`, `input_lufs`, `output_lufs`,
`peak_dbfs`, `clipped_samples`, `manifest_alignment_samples`,
`output_sha256`.

The manifest records every segment with `ok`, `missing`, `truncated`,
`corrupt` or `replayed` status plus `missing_ranges`, all on the same output
sample clock as the MP4. `stats.json` also carries `bad_details`, listing each
rejected record's byte offset, recoverable sequence number, error code and
message. The 5 % bitrate tolerance is enforced for clean streams; loss and
corruption inherently lower the received rate, but alignment is always
checked.

## Architecture

```
cmd/mediacheck            CLI: generate | run | verify
internal/packet           RTP/RTCP framing, validation, capture reader/writer
internal/clock            exact rational time, 32-bit wrap and 16-bit seq expansion
internal/timeline         watermark, dedupe/reorder, loss gaps, late replay
internal/audio            rational resampler, layout normalization, EBU R128 meter and limiter
internal/mux              deterministic MP4 writer (ipcm PCM track, rebuilt index)
internal/manifest         stable JSON timeline
internal/quality          statistics, tolerances, SHA-256
internal/pipeline         end-to-end conversion
internal/fixture          deterministic capture generator
internal/mediaerr         structured error model (code, offset, seq, range)
```

Data flow: RTP/RTCP parsing -> clock mapping -> watermark and replay -> audio
processing -> MP4 remux and manifest -> quality statistics.

## Design notes

- **One media clock.** RTP timestamps are expanded across wrap and mapped to
  output samples with exact integer arithmetic (`round(x*out/in)`), so the
  manifest, the muxer frame count and the audio agree within one sample.
  Input is split into payload-sized blocks but the whole signal is resampled
  once, so chunk boundaries cannot accumulate drift.
- **Deterministic by construction.** No wall-clock values, no map iteration in
  output paths, no floating-point decisions about output length. Re-running on
  the same input reproduces identical statistics, manifest bytes and MP4
  SHA-256; a reordered capture produces the same MP4 as the ordered one.
- **Late packets.** The watermark advances by the configured delay. A late
  packet is laid at its true timestamp and overwrites the placeholder in
  arrival order; the affected interval is merged and reported as `replayed`.
- **Bounded damage handling.** Captures are limited to 2 GiB and records to
  64 KiB; lengths above those limits are rejected before allocation. A
  corrupt or truncated packet keeps its sender-declared duration, is replaced
  with silence, and is listed in the manifest while later packets continue.
- **Loudness.** BS.1770-style gating over 400 ms blocks with 75 % overlap;
  gain is capped by a sample-peak ceiling, so output never clips.

## Limitations

- Payloads are interpreted as interleaved 16-bit PCM (the fixture format).
  Compressed codecs such as Opus/AAC are out of scope.
- Mono and stereo only; 5.1 and other layouts are rejected.
- The limiter is a sample-peak clamp, not a true-peak oversampled limiter.
