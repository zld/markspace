# medialab

A deterministic, local media-processing kernel for a mobile low-latency
interactive A/V pipeline. Inputs are a per-frame manifest plus RTP fragments of
synthetic RGB frames; the kernel merges multi-track timelines on one nanosecond
clock, plans parameters under explicit budgets, exports frame files and MP4
segments offscreen, and re-verifies the artifacts from disk.

No device, network or external service is touched. Everything is generated from
the numeric seed, so a rerun with the same seed and parameters reproduces every
hash.

## Requirements

- macOS (Darwin) with Go 1.23 or newer.
- `github.com/abema/go-mp4 v1.0.0` and `github.com/pion/rtp v1.8.9`, pinned in
  `go.mod`.

> Note: the brief asked for `go-mp4 v1.0.1`, but that tag does not exist
> upstream; the module's only v1.0.x release is `v1.0.0`, which is what is
> pinned here.

## Build and test

```sh
go mod tidy
go build ./...
go test ./...
```

## Run

```sh
go run ./cmd/medialab demo --seed 7 --budget-ms 500 --max-bytes 1048576 --workers 2
```

The report prints the chosen plan, per-frame SHA-256, segment hashes, pixel
mean and variance, peak loudness in dBFS, maximum alignment error in ns, actual
artifact bytes, peak cache bytes and processing time. The independent verifier
then re-reads the artifacts and prints its own re-derived numbers.

Repeat runs with a fixed seed print an identical `run hash`.

### Cases

| `--case`              | Expectation                                                  |
|-----------------------|--------------------------------------------------------------|
| `normal`              | exit 0, full report                                          |
| `corrupt-hash`        | exit 1, first error names track 2 frame 2                     |
| `reorder-loss`        | exit 0, in-window reorder repaired, fill frames recorded     |
| `timestamp-regression`| exit 0, regression reported as a recoverable warning         |
| `budget-conflict`     | exit 1, conflict reason and candidate summary listed         |
| `cache-boundary`      | exit 0, peak cache printed and below the configured cap      |

`budget-conflict` clamps the time and byte budgets, and `cache-boundary` clamps
the cache cap, so each sample actually exercises its boundary; the clamped
value is printed.

### Flags

`--seed`, `--budget-ms`, `--max-bytes`, `--workers`, `--cache-bytes`,
`--reorder-window`, `--max-dim`, `--min-quality`, `--align-ns`, `--out`,
`--keep-input`. Every processing boundary is configurable and echoed in the
report.

## Exit codes

| Code | Meaning                                    |
|------|--------------------------------------------|
| 0    | success, or recoverable warnings only      |
| 1    | input hard error (manifest, RTP, planning) |
| 2    | verification failure (artifact constraints)|

## Layout

```
internal/model      nanosecond time base, frame metadata, structured faults
internal/ingest     manifest validation + SHA-256, RTP unwrap/reorder/assemble
internal/timeline   stable multi-track merge and alignment error
internal/planner    constrained candidate selection with explicit conflicts
internal/worker     bounded-concurrency pool with a byte cache budget
internal/render     deterministic offscreen frame and MP4 segment export
internal/verify     independent re-read and constraint checking
internal/pipeline   stage wiring shared by the CLI and end-to-end tests
cmd/medialab        terminal entry point
```

## Determinism notes

- All synthetic content is a pure function of the seed and case name.
- RTP payloads are reassembled before decoding, so reordering cannot smuggle a
  different frame in; rendered frames are proven against the manifest identity.
- The planner prices candidates from real rendered PNG sizes, so a byte budget
  is enforced against what is actually written.
- The verifier re-hashes every frame and segment, rebuilds the run hash from
  those hashes, and rebuilds timing from each segment's `stts` table. A manifest
  that merely claims good numbers does not pass.
