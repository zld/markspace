# archivist

A reusable Go media-processing kernel for long-term MP4/MOV archiving. It
repairs broken presentation timelines, rebuilds random-access indexes, and
records where every repaired timestamp came from — deterministically, with
resumable checkpoints.

The kernel never re-encodes media. It reads container structure, rewrites the
sample tables, copies payload bytes verbatim, then verifies the result.

## Quick start

```sh
brew install go
go mod tidy
go build ./...
go test ./...
go run ./cmd/archivist demo --dir ./tmp/demo --queue 4
```

The demo generates its own fixtures (no external media), runs the pipeline,
and prints a JSON report with these fields per input:

| Field | Meaning |
| --- | --- |
| `status` | `ok`, `error`, `interrupted`, or `cancelled` |
| `error_offset` | absolute byte offset of the first unparseable box (`-1` if none) |
| `repaired_pts` | sample of changed timestamps with original, repaired and delta |
| `index_entries` | samples described by the rebuilt random-access index |
| `resume_ms` | wall time spent in this invocation |
| `output_sha256` | digest of the rebuilt MP4 |
| `provenance_sha256` | path-independent digest of the provenance record |

The report also carries `first_run_ms`, `resume_run_ms`, `resume_budget_ms`
(80% of the first run), `interrupted_at`, and the `queries` used to check
random-access accuracy.

```sh
# Repair arbitrary files
go run ./cmd/archivist run --out ./tmp/work --queue 4 a.mp4 b.mov

# Exit-code contract for a single file, suitable for scripts
go run ./cmd/archivist check --out ./tmp/check a.mp4

# Resolve a time to its sample and keyframe
go run ./cmd/archivist query --index ./tmp/work/index/a.json --track 1 --time 45000
```

Exit codes: `0` success, `2` corrupt or unrepairable input, `1` any other
failure.

> **Note on `go run`.** Go documents that `go run`'s exit status is not the
> child's: a program exiting 2 surfaces as shell status 1. The `demo` command
> therefore reports per-input outcomes in its JSON (`exit_code`) and exits 0
> once every contract check passes. Use a built binary
> (`go build -o archivist ./cmd/archivist`) or `check` when you need the raw
> 0/2/1 status in a shell.

## Acceptance script

`./scripts/acceptance.sh` runs the documented build, vet and test commands,
then asserts every acceptance criterion against the demo report and per-file
exit codes. It prints one `PASS`/`FAIL` line per check.

## Layout

```
cmd/archivist/       CLI: demo, run, check, query
internal/clock/      media timebase, exact tick scaling, RTP wrap expansion
internal/fault/      offset/track/stage-aware error model and exit codes
internal/scan/       streaming box walker and sample-table decoder
internal/repair/     monotonic timeline repair with recorded corrections
internal/index/      random-access index rebuild and query
internal/pipeline/   bounded queue, checkpoints, provenance
internal/fixture/    deterministic MP4 fixtures and controlled corruption
```

## How it works

### 1. Streaming scan

`scan.Reader` walks box headers with `ReadAt`, falling back to `moov`-only
decoding. Payload bytes are never buffered. Three failure classes are kept
distinct:

* **Truncated header** (fewer than 8 bytes remain) → corrupt, offset =
  position of the incomplete header.
* **Length overrun** (declared size exceeds the parent's extent) → corrupt,
  offset = the box header that promised too much.
* **Unknown but well-formed box** → skipped, recorded in `skipped`, with a
  warning. Known container types (`moov`, `trak`, `mdia`, `minf`, `stbl`,
  `dinf`, `edts`) are descended; unknown leaves are skipped without buffering.

The first fault wins and carries the absolute byte offset, so a reported
offset is the first byte that cannot be trusted — a 0-byte deviation from the
corrupted header the fixture injected.

### 2. Unified clock and repair

All track timestamps are 32-bit patterns. `clock.Unwrapper` expands them onto
a monotonic 64-bit timeline using the RTP nearest-candidate rule
(`|delta| ≤ 2^31`), flagging only forward crossings of 0xFFFFFFFF as wraps.
`clock.ScaleTicks` converts between timescales with exact big-integer
arithmetic and half-away-from-zero rounding, so results never drift between
platforms or runs.

`repair` then enforces strict monotonicity per track:

* **wrap** — the stored value moved backwards across 2^32; the expanded value
  continues the cadence and the change is recorded.
* **duplicate / small backward** — clamped to `previous + step`.
* **large backward** (more than `MaxBackwardTicks`, default one second) — the
  repair value cannot be determined, so processing stops with `KindTimestamp`
  carrying the track, timescale and sample offset. No success status is
  written.

Every entry keeps `Original`, `Repaired`, `Delta` and a `Kind`, so the
provenance record can show exactly what changed and why.

### 3. Rebuilt index

`index.Rebuild` writes a normalized file: `ftyp | moov | mdat`, one chunk per
sample (configurable), payload bytes copied unchanged. Because a repaired
timeline may exceed 2^32 ticks, on-disk timestamps are rebased to start at
zero; the rebase amount is recorded per track and the index keeps absolute
times. The written file is immediately re-scanned and compared against the
plan (`index/verify.go`), so a writer bug fails the run instead of producing a
silently wrong index.

* `Locate(track, t)` returns the sample containing `t` (never more than one
  sample step late) plus the keyframe decoding must begin at.
* `Query(track, t)` returns the nearest keyframe at or before `t`.

### 4. Bounded pipeline

`pipeline.Runner` feeds inputs through a channel of exactly `QueueCapacity`
and runs that many consumers. A full queue blocks the producer; total memory
is therefore bounded by capacity times fixed block size, independent of input
count or file length. A downstream failure cancels the shared context, which
stops the producer and halts in-flight inputs at their next stage boundary.
Corrupt inputs are per-input outcomes: they are diagnosed individually and do
not cancel the batch.

### 5. Checkpoints and provenance

Six stages are durable: `scan → repair → write → index → verify → provenance`.
Each completed stage records a stage version and, where it produces bytes, an
artifact with its size and SHA-256.

A checkpoint is keyed by input SHA-256 plus a hash of all run parameters
(queue capacity, chunk size, movie timescale, repair limit, all stage
versions). Different bytes or parameters therefore get a different checkpoint;
they never resume stale state.

Resume rules:

* Stages whose artifacts verify are skipped; their artifacts are re-read
  rather than recomputed.
* If an artifact's digest no longer matches, that stage and everything after
  it are truncated and rerun. Completed work before it is kept.
* An interrupted or failed run leaves its checkpoint and no published output;
  temporary files are never renamed into place on failure.
* Rerunning the same input and parameters executes no stages and reproduces
  identical output and provenance digests.

Provenance records the input digest, parameter hash, rules version, per-track
correction list, entry counts and output digest. Its `digest` field excludes
absolute paths, so the same input processed in two work directories yields the
same `provenance_sha256`.

## Fixtures

`fixture.BuildAll` writes ten deterministic samples and a `manifest.json`:

| Kind | What it exercises |
| --- | --- |
| `normal` | clean 30fps timeline |
| `truncated` | `moov` payload cut short; extent exceeds EOF |
| `truncated_header` | trailing box header cut after five bytes |
| `bad_length` | `mdat` size rewritten far past EOF |
| `wrap` | presentation timestamps cross 2^32 |
| `regression` | timestamps step backwards mid-track |
| `start_offset` | non-zero starting presentation time |
| `duplicate` | a repeated presentation timestamp |
| `multitrack` | video at 90 kHz plus an audio track at 48 kHz |
| `wrap_regression` | wrap followed by a backward jump |

Sample payload bytes are keyed on the fixture name, so two fixtures never
share an output digest.

## Scope

Supported: recognizable MP4/MOV container structure; tracks that declare a
timescale. Not supported: transcoding, payload repair, inferring missing
content, `co64` output (files beyond 4GiB), or fragmented (`moof`) media.
