# mediacheck

Terminal-only Matroska/WebM programme-master inspection and remuxing tool for
broadcast content engineering. It scans self-generated EBML fixtures, binds a
persistent index to a byte-level input summary, synthesises a common-clock AV
timeline, normalises audio layout/sample rate and colour metadata, exports
deterministic frame hashes, recovers lossy RTP return feeds, and republishes a
minimal valid MP4 only when every release gate passes.

There is no GUI, browser, camera or microphone dependency. All test data is
generated in-process from a fixed seed; no external files are referenced.

## Requirements

- Go 1.23 toolchain (developed and verified on macOS/darwin)
- Purely local; no network or external service at runtime

## Dependency note

The brief pins `github.com/abema/go-mp4 v1.24.2`, but that tag does not exist
on the upstream module or the Go proxy (the highest published release is
`v1.7.4`, confirmed via both `GOPROXY=direct` and the VCS tag list). The
closest available release, **`github.com/abema/go-mp4 v1.7.4`**, is pinned
instead. `github.com/pion/rtp v1.8.9` is pinned exactly as specified. Run
`go mod tidy` to populate `go.sum`.

## Build and test

```sh
go mod tidy
go build ./...
go test ./...          # add -race for the race detector
```

## Demo

Healthy path — exits `0`, AV sync ≤ 20 ms, RTP recovery rate ≥ 0.9:

```sh
go run ./cmd/mediacheck demo --seed 17 --frames 240 --loss 0.02 --jitter-ms 8
```

Corrupt path (truncated cluster + duplicated channel layout + heavier loss) —
exits `2`, reports the truncation offset and layout fault, and never writes a
completion marker:

```sh
go run ./cmd/mediacheck demo --seed 17 --truncate cluster \
  --mutate-audio-layout --loss 0.08 --jitter-ms 24
```

> Note: `go run` reports a child process exit code as `exit status 2` and
> itself returns `1`. Build the binary (`go build -o mediacheck ./cmd/mediacheck`)
> to observe the program's own `0`/`1`/`2` exit codes directly.

Other entry points:

```sh
mediacheck scan    <input.mkv> --index-dir DIR
mediacheck process <input.mkv> --index-dir DIR --output OUT.mp4 --frames N
```

## Exit codes

| Code | Meaning                                                        |
|------|----------------------------------------------------------------|
| 0    | Completed and every metric within its limit                    |
| 1    | Bad parameters or runtime/IO failure                            |
| 2    | Corrupt, unsupported, or unrecoverable input                    |

## Report fields

`input_sha256`, `frames`, `audio_samples_in`, `audio_samples_out`,
`audio_duration_error_ns`, `av_sync_error_ms`, `rtp_lost`, `rtp_recovered`,
`rtp_unrecoverable`, `recovery_rate`, `p95_latency_ms`, `index_rebuilt`,
`frame_hashes`, plus integrity/provenance fields (`truncated_at`,
`crc_failures`, `timeline_trusted`, `audio_layout_valid`, `color_source`,
`color_unsafe_reason`, `published`, `completion_marker`, `faults`).

Identical inputs and parameters always produce identical `input_sha256` and
`frame_hashes` across runs.

## Package layout

| Package                 | Responsibility                                                  |
|-------------------------|------------------------------------------------------------------|
| `timeline`              | Nanosecond clock, rational timestamp math, error model, AV merge |
| `ebml`                  | Streaming EBML reader (VINT, unknown-length, depth limit), scan |
| `index`                 | SHA-256-bound persistent index, atomic write, invalidation       |
| `audio`                 | Channel-layout resolution, resample planning and PCM conversion  |
| `color`                 | Source-priority colour conflict resolution, conversion guard     |
| `render`                | Headless fixed-time frame extraction and SHA-256 hashing         |
| `rtp`                   | Bounded-window RTP reorder/loss recovery via pion/rtp            |
| `pipeline`              | Cancellable end-to-end orchestration and MP4 remux gates         |
| `internal/fixture`      | Deterministic MKV-style generator, fault injection, RTP feed     |
| `cmd/mediacheck`        | Terminal CLI                                                     |

## Safety limits

Input size (512 MiB), EBML element payload (256 MiB), nesting depth (16),
track count (32), RTP reorder window, and integer arithmetic are all bounded;
path traversal and overflow are rejected rather than truncated silently.
