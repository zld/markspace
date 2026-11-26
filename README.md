# rtp-regress

Offline regression tool for weak-network game voice traces. It recovers a
monotonic, integer timeline from length-prefixed RTP/MPEG-TS input, normalizes
the audio through the local FFmpeg binary, and emits fragmented MP4 plus a
recomputable quality manifest. Everything runs locally; no network services or
external sample files are used.

## Build

```sh
go mod tidy
go build -o rtp-regress ./cmd/regress
go test ./...
```

Go 1.23 and FFmpeg (with `aac`, `loudnorm`, `ebur128`, `aresample`, `pan` and
fragmented MP4 muxing) must be installed.

## Usage

Generate the self-contained fixture traces:

```sh
rtp-regress -generate ./tmp/cases
```

Run a regression over one trace:

```sh
rtp-regress -in ./tmp/cases/clean.rtp -out ./tmp/clean \
  -rate 48000 -channels 1 -bitrate 96000 -workers 4
```

Fault handling and cancellation:

```sh
# isolate faults and keep producing output (exit 0)
rtp-regress -in damaged.rtp -out out -policy isolate
# strict: first input fault is a stable error (exit 2)
rtp-regress -in damaged.rtp -out out -policy strict
# cancel after two segments, save a checkpoint (exit 75)
rtp-regress -in cancel.rtp -out out -cancel-after 2
# resume from the checkpoint; identical manifest hash to a fresh run
rtp-regress -in cancel.rtp -out out -workers 1 -resume
```

## Outputs

- `output.m4s` — fragmented MP4, mono 48 kHz, AAC 96 kbps, validated with go-mp4
- `manifest.json` — segment timeline, isolated gaps (PID/offset/time range/code),
  PSI version history, RTP reorder counters, drift and quality metrics
- `audio/seg_*.pcm` — canonical per-segment PCM (checkpoint/determinism source)
- `checkpoint.json` — content-addressed resume state

## Quality gates (clean input)

| Metric | Threshold |
| --- | --- |
| bitrate delta | within 5% of 96 kbps |
| SNR | >= 15 dB |
| integrated loudness | within 1 LU of -16 LUFS |
| true peak | <= -1 dBTP |
| manifest timeline error | <= 20 ms |

## Architecture

```
internal/clock    integer nanosecond/sample time base, PCR/RTP unwrap, drift
internal/rtp      length-prefixed trace reader, 64-window reorder
internal/ts       188-byte aligned demux, PSI/PCR/PES, CC and policies
internal/audio    ffmpeg decode, program loudnorm, single-pass encode, metrics
internal/pipeline bounded worker pool, deterministic order-independent merge
internal/manifest sorted JSON report and go-mp4 fMP4 timing validation
internal/fixture  local Go trace generator for every fault class
internal/app      end-to-end orchestration, checkpointed cancel/resume
cmd/regress       thin CLI flag wrapper
```

Safety: concurrency and queue depth are bounded, payloads are never passed
through a shell, and input content is never executed as commands.
