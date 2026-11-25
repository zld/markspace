# archivecheck — deterministic media archive kernel

A reusable Go 1.23 processing kernel for long-term media archive and historical
asset recovery. All samples are generated in code; the demo and tests do not
read external media, subtitles, RTP captures, or MP4 fixtures.

The common basis is:

- nanosecond timestamps via [`internal/clock`](internal/clock);
- structured `phase`, `sample`, and byte `offset` errors via
  [`internal/diagnostic`](internal/diagnostic);
- bounded, ordered, cancellable workers via
  [`internal/pipeline`](internal/pipeline);
- stable SHA-256 summaries that exclude wall duration and completion order.

Pinned dependencies are `github.com/abema/go-mp4 v1.4.0` and
`github.com/pion/rtp v1.8.11`.

## Modules

| Path | Responsibility |
| --- | --- |
| `internal/clock` | 24/25/29.97 DF/NDF/50 fps timecodes and stable nanosecond timeline |
| `internal/subtitle` | SRT/WebVTT parsing, byte offsets, line/input/cue limits |
| `internal/media` | go-mp4 probing, generated track descriptions, layout rules, pion RTP validation |
| `internal/policy` | Quality/bitrate/budget decisions with explicit reasons and infeasible results |
| `internal/pipeline` | Bounded worker pool, deterministic merge, cancellation and resume |
| `internal/report` | Input/output mappings, failure locations and stable digest |
| `internal/samplegen` | Deterministic code-generated valid and invalid samples |
| `cmd/archivecheck` | Terminal demo/acceptance adapter |

## Limits

The kernel deliberately bounds untrusted work:

- maximum 8 workers and 64 queued tasks;
- maximum 10,000 jobs/subtitle cues;
- subtitle input ≤ 1 MiB, line ≤ 4096 bytes;
- MP4 probe input ≤ 8 MiB;
- supported channel layouts: mono, stereo, 5.1, 7.1;
- supported sample rates: 8 kHz–192 kHz.

Channel, sample-rate, or color mismatches are never guessed. They are
`preserve`, `remap` only with an explicit mapping/resampler/color rule, or
`reject` with a reason.

## Build and test

```sh
brew install go@1.23
go mod tidy
go build ./...
go test ./...
./scripts/build.sh
```

`scripts/test.sh` uses `/opt/homebrew/opt/go@1.23/bin/go` when the Homebrew
`go@1.23` formula is installed.

## Demo acceptance

```sh
go run ./cmd/archivecheck demo \
  --seed 17 --workers 3 --budget-bytes 1048576
```

The batch contains seven valid mappings and six injected failures:

- truncated subtitle;
- illegal 29.97 DF frame number;
- channel-layout metadata conflict;
- bad RTP timestamp;
- oversized subtitle line;
- budget overrun.

Each item prints `input`, `output`, `time_delta_ns`, `byte_delta`, byte counts,
and policy estimates/reasons. Successful items have both deltas equal to zero.
The total successful output is below 1,048,576 bytes. Failed items print
phase, stable code, sample number, and byte offset; other items continue.

Compare concurrency:

```sh
go run ./cmd/archivecheck demo --seed 17 --workers 1 --budget-bytes 1048576
```

The `stable_digest` and all `item ...` mapping lines are identical for workers
3 and 1.

Cancellation and deterministic resume:

```sh
go run ./cmd/archivecheck demo --seed 17 --workers 3 --cancel-after 2
```

This prints `completed`, `canceled`, and `not_started` counts, reruns the
unfinished indices, and prints matching `resume_digest` / `full_digest` values
with `resume_matches_full=true`.

The full acceptance script builds a binary and checks exact process exit codes:

```sh
./scripts/acceptance.sh
```

## Exit codes

The command's `run` function uses:

- `0`: success, including `demo --valid-only`;
- `2`: all samples ran, but expected injected sample failures are present;
- `64`: invalid command-line arguments;
- `1`: internal/merge/budget accounting failure.

The compiled `bin/archivecheck` and `scripts/acceptance.sh` preserve these
exact codes. Standard `go run` wraps any non-zero child status in the Go tool's
own exit code `1` while printing `exit status 2` or `exit status 64`; use the
built binary when a shell harness must assert the numeric contract directly.

## Stable summary

The digest is computed over ordered mappings, counts, budgets, and deterministic
configuration. It intentionally excludes wall-clock duration, goroutine
completion order, and the `--workers` value.
