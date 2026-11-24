# mediarun

A reusable, deterministic media-processing kernel for long-term MPEG-TS archive
ingest and historical-asset recovery. It parses damaged transport streams,
corrects PCR/PTS timeline problems, normalizes audio to a fixed target, and
makes every result resumable and traceable to its exact input bytes.

The kernel runs fully locally on macOS with no service or GUI dependency. All
sample data is generated in code; no external media is required.

## Toolchain

- Go 1.23 (module `mediarun`, dependencies pinned in `go.mod`)
- FFmpeg 7.1.1+ for resampling, channel mapping and loudness
- `github.com/abema/go-mp4@v1.4.1` for MP4 timing/sample structures
- `github.com/pion/rtp@v1.8.13` for RTP sequence/loss handling

```sh
brew install go@1.23 ffmpeg
go mod tidy
go build ./...
go test ./...
```

## Layout

| Package        | Responsibility                                                        |
|----------------|-----------------------------------------------------------------------|
| `internal/clock`     | 33-bit PCR/PTS unwrap, shared 90 kHz time base, error-event model |
| `internal/ts`        | 188-byte packet parse/validate, continuity counters, PAT/PMT, PES  |
| `internal/rtp`       | Sequence-window reorder, duplicate/late/loss accounting            |
| `internal/audio`     | 48 kHz stereo, two-pass EBU R128 loudness at -16 LUFS             |
| `internal/pipeline`  | Content cache, atomic checkpoints, digest-chained provenance       |
| `internal/mp4info`   | Read-only MP4 timeline/sample structure via go-mp4                 |
| `internal/fixture`   | Deterministic TS/RTP damage scenarios                              |
| `internal/engine`    | Wires ingest → timeline → audio → cache/checkpoint/provenance      |
| `cmd/mediarun`       | Terminal acceptance entry point                                     |

## Usage

Self-contained acceptance run (normal, PCR wrap, CC jump, PSI update, loss,
truncated and damaged fixtures):

```sh
go run ./cmd/mediarun verify --self-fixtures --out /tmp/media-check
```

Interruptible, resumable processing of a damaged fixture:

```sh
# stop after two segments; work and digests are already checkpointed
go run ./cmd/mediarun run --fixture damaged --checkpoint /tmp/media-checkpoint --interrupt-after 2
# resume without the interrupt flag: no segment is emitted twice
go run ./cmd/mediarun run --fixture damaged --checkpoint /tmp/media-checkpoint
```

Exit codes: `0` success, `1` input/processing failure, `2` argument error.

## Guarantees

- PCR/PTS are unwrapped on a 33-bit monotonic clock; a wrap never moves time
  backwards and undecidable jumps become error events with PID and byte range.
- RTP reordering never fabricates media: dropped sequence numbers stay absent;
  out-of-window late packets count as loss.
- A cache key covers input digest, processing parameters and tool version; a
  hit still re-verifies source and artifact digests before reuse.
- Checkpoints commit only after a segment artifact and its digest are durable,
  so an interrupted run resumes idempotently and yields the identical digest.
- Outputs and checkpoints are confined to given roots; `..` traversal is refused.
- Unrecoverable/unsupported inputs (missing audio, unknown channel layout)
  fail explicitly rather than producing guessed media.
