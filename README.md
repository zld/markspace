# rtp-rebuild

Offline reconstruction of a client event log and a damaged RTP/RTCP capture onto
a single 90 kHz media timeline, with a deterministic manifest and itemized
validation of every artifact.

The tool is a pure terminal program: it takes run parameters, writes artifacts
and a manifest under `--out`, and prints one line per result. There is no GUI and
no external service.

## Build and test (macOS)

Requires Go installed from the [go.dev macOS installer](https://go.dev/dl/).

```sh
go mod tidy
go mod download
go build ./...
go test ./...
```

### Note on the `go` directive

`go.mod` declares `go 1.24.0` and pins the two required dependencies:

```
github.com/abema/go-mp4 v1.7.4
github.com/pion/rtp   v1.10.6
```

The task asked for a `go 1.23` directive. Go's module rules, however, force the
main module's language version up to the highest `go` directive among its
dependencies, and `github.com/pion/rtp v1.10.6` declares `go 1.24.0`. This was
verified with a local Go 1.23.12 toolchain against a `go 1.23` main module:

```
$ GOTOOLCHAIN=local go1.23.12 build ./...
go: github.com/pion/rtp@v1.10.6 requires go >= 1.24.0 (running go 1.23.12)
```

There is no way to keep the pinned pion/rtp version and a `go 1.23` directive.
The directive is therefore `go 1.24.0`; the code itself uses no API newer than
Go 1.21 (only `encoding/json`, `crypto/sha256`, `context`, `sort`, `os`).
Earlier pion/rtp releases (v1.10.1 and below) do declare `go 1.21`, but the task
pins v1.10.6, so the version was kept.

## Running the self-test

```sh
go run ./cmd/rebuild --self-test --seed 7 --budget-ms 10000 --out /tmp/rtp-rebuild
```

The command generates every fixture itself (nothing is read from disk):

| fixture | damage |
| --- | --- |
| `normal` | none: 100 packets in order |
| `fault`  | reordered packets, duplicates, two missing packets, one 700 ms stall, a 32 bit timestamp wrap and a truncated RTCP report |

Both fixtures are also written as `input/normal.rtrb` and `input/fault.rtrb`
under `--out`.

### Output

The flat block is the acceptance list. Values aggregate both fixtures; the
per-stream block that follows gives the same numbers per stream with a
`stream.<name>.` prefix, including the exact truncation offset.

```
packets=161
unique=158
duplicates=3
loss=2
loss_rate=0.0125
jitter=287.25
gap_intervals=1
offset_errors=1
peak_buffer_bytes=9600
elapsed_ms=564
budget_ms=10000
budget_delta_ms=-9436
manifest_sha256=...
artifact_sha256=...
resume_stage=complete
clock_offset=known
unalignable_events=1
item_results=26/26 pass=21 expected_damage=5 fail=0
stream.normal.packets=100
...
stream.fault.truncated_offset=3130
interval stream=fault start_seq=65520 end_seq=65521 start_ms=1333 end_ms=2033 gap_ticks=63000 events=2
unalignable stream=fault line=6 kind=stall reason=...
item normal.mp4_sample_count PASS samples=100 expected=100
...
result=PASS
```

* The healthy stream reports `unique=100 duplicates=0 loss=0 offset_errors=0`
  and every item passes.
* The fault stream reports at least one gap interval, a precise truncation
  offset and the damaged items as `EXPECTED` (the fixture is meant to be
  damaged), while all other items still pass.
* `budget_delta_ms` is `elapsed_ms - budget_ms`.

### Exit codes

| code | meaning |
| --- | --- |
| 0 | every threshold passed |
| 1 | a business threshold failed (loss rate above 2 %, any gap interval, time budget exceeded, artifacts withheld) |
| 2 | unrecoverable parameter or IO error |
| 3 | a partial but usable trajectory was produced and the damage is locatable (an input offset is reported) |

With `--stream`, a damaged capture that still yields a validated track exits 3;
its manifest is published and the damage is listed item by item.

## Reproducibility and resume

The manifest is canonical JSON with fixed field order and no direct map
serialization, so the same input produces byte-identical manifests and artifact
hashes.

```sh
# Same command again: same manifest and artifact hashes.
go run ./cmd/rebuild --self-test --seed 7 --budget-ms 10000 --out /tmp/rtp-rebuild
# resume_stage=complete, item manifest_repeatable PASS

# Stop after mux, then resume from validate.
go run ./cmd/rebuild --self-test --seed 7 --budget-ms 10000 --out /tmp/rtp-rebuild --interrupt-after mux
go run ./cmd/rebuild --self-test --seed 7 --budget-ms 10000 --out /tmp/rtp-rebuild --resume
# second run prints resume_stage=complete and the same hashes as a clean run
```

A checkpoint is reused automatically when it describes exactly the same inputs
(same stream digests, seed and budget); otherwise the run starts fresh. An
explicit `--interrupt-after` always runs from the beginning so the interruption
point is real.

## Processing a real capture

```sh
go run ./cmd/rebuild --stream capture.rtrb --out /tmp/out
```

## Input format

A stream is a 16 byte header followed by framed records:

```
magic "RTRB" | version 1 | reserved | capture start unix-nanos (8 bytes BE)
record: kind (1) | flags (1) | payload length (4 bytes BE) | payload
```

Record kinds: `1` RTP packet, `2` RTCP compound packet, `3` client event line.
The client event line syntax is `<time> <kind> [key=value ...] [text]`, where
`<time>` is Unix milliseconds, Unix seconds, RFC 3339, or a `+` relative offset
(`+1500ms`, `+90000t`, `+1.5s`).

## Design

| package | responsibility |
| --- | --- |
| `internal/clock` | 90 kHz tick type, overflow-checked arithmetic, RTP timestamp unwrapping |
| `internal/fault` | structured errors carrying stage, input byte offset and sequence |
| `internal/ingest` | bounded record reader: 4 MiB stream cap, byte-bounded queue, backpressure, cancellation |
| `internal/rtp` | header parsing, duplicate counting, extension-window reordering, loss runs, out-of-window anomalies |
| `internal/rtcp` | sender/receiver report blocks, signed cumulative loss, jitter, structured truncation errors |
| `internal/events` | client event parsing; unparseable events kept and marked unalignable |
| `internal/align` | event/media alignment, 18000-tick stall intervals with both sequence numbers and times |
| `internal/stage` | ordered stages, checkpointing, soft vs hard failure, resume, budget |
| `internal/manifest` | canonical deterministic manifest with per-stage digests |
| `internal/mp4` | MP4 track at the 90 kHz timescale, one sample per reconstructed payload |
| `internal/validate` | MP4 structure, hash, memory, budget and threshold items |
| `internal/sample` | deterministic fixtures, built in-process |
| `cmd/rebuild` | CLI, pipeline wiring, report and exit codes |

### Behavior notes

* **Backpressure and cancellation.** Records are queued on a channel bounded by
  32 entries and 1 MiB of payload. The reader blocks rather than buffering the
  whole file; cancellation closes the input and the completed stages stay on
  disk for resume.
* **Reordering.** A fixed 256-entry window ahead of the emit cursor recovers
  out-of-order packets. Duplicates are counted once. Once the window passes a
  missing number it is recorded as loss and a later arrival is recorded as an
  anomaly, never as a fabricated packet.
* **Timestamps.** 32 bit RTP timestamps are unwrapped across the wrap boundary.
  Media time zero is the first reconstructed packet, so relative event offsets
  and packet times share an origin.
* **Stalls.** Two consecutive valid packets further apart than 18000 ticks
  (200 ms) form one interval, carrying the first and last sequence numbers,
  start and end times, and the events inside it.
* **Unalignable events.** An event with no usable timestamp, or a wall clock
  event when the capture start is unknown, is kept with its original text and a
  reason. It never aborts the other events; the report prints
  `clock_offset=known|unknown`.
* **Publication.** Tracks are written to `staging/`, validated, and only then
  renamed into place. If an integrity item fails the staged files are discarded
  and `artifacts=withheld` is printed. All writes use a temporary file and an
  atomic rename.
* **Budgets.** The fixed envelope is 4 MiB of input and 256 pending packets.
  Exceeding either fails the run without publishing a partial artifact.
