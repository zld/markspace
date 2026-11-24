# medcheck

`medcheck` is a local, deterministic Go media kernel for regional live-stream
distribution. It accepts generated RTP/RTCP-like fixtures, organizes sequence
numbers, buffers reordering, recovers allowed gaps, synchronizes audio/video on
a shared nanosecond clock, adapts AAC/H.264 metadata, writes an MP4 with
`go-mp4` validation, and resumes from checksummed packet-boundary checkpoints.

No external media assets or network services are required.

## Build and test

```bash
brew install go   # if Go 1.23+ is not already installed
go mod tidy
go build ./...
go test ./...
```

A full local verification is available:

```bash
./scripts/verify.sh
```

## Demo commands

```bash
go run ./cmd/medcheck demo --seed 17 --loss 4,9 --reorder 3 --checkpoint-every 5
go run ./cmd/medcheck demo --seed 17 --truncate-at 7 --resume
```

The JSON report includes:

- `packets`, `duplicates`, `reordered`
- `recovered`
- `av_drift_ms`, `av_corrections`
- `first_bad_offset`
- `resume_ms`
- `sha256`
- stable `error_codes`

Repeated runs with the same flags produce the same `sha256`. A truncate/resume
run compares its checksum against a complete in-memory run and fails if they
differ.

## Generated edge cases

```bash
go run ./cmd/medcheck cases
```

This emits results for multichannel audio, sample-rate conflict, missing and
conflicting H.264 color metadata, and a truncated RTP packet.

## Checkpoints

Checkpoints are JSONL records. Each record contains a complete serializable
state and its SHA-256. If the last record is corrupt, loading returns
`CHECKPOINT_INVALID` together with the newest valid boundary. If no record is
valid, it returns `CHECKPOINT_NO_VALID_BOUNDARY`.

```bash
go run ./cmd/medcheck checkpoint verify --path .medcheck/checkpoint.jsonl
go run ./cmd/medcheck checkpoint corrupt --path .medcheck/checkpoint.jsonl
```

## Exit codes

- `0`: success, including input corruption recovered by policy
- `2`: unrecoverable media gap
- `3`: invalid checkpoint with no usable resume boundary
- other non-zero: usage, I/O, or checksum-equivalence failure

## Package map

- `internal/clock`: shared nanosecond timeline, RTCP mapping, comparable errors
- `internal/rtp`: pion RTP parsing, SSRC demux, 16-bit sequence unwrapping
- `internal/jitter`: packet/time windows and gap events
- `internal/recovery`: audio PLC/silence policy and video keyframe gate
- `internal/syncer`: 80 ms drift threshold and monotonic correction clamp
- `internal/adapter`: exact `aac-v1` / `h264-v1` registry and metadata policies
- `internal/checkpoint`: checksummed JSONL boundary log
- `internal/output`: deterministic MP4 writer plus `go-mp4` validation
- `internal/sample`: code-generated seeded fixtures
- `internal/pipeline`: deterministic orchestration and edge cases
