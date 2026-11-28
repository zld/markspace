# mediasync

Offline validator for multilingual game dialogue, subtitles and sound-effect
assets delivered as timestamped MP4/MOV containers plus RTP packet streams.
It reconciles tracks that use different time bases, detects clock drift and
timestamp wraparound, handles variable frame rates from real sample
durations, validates container/chunk boundaries and recovers single packet
loss from in-band XOR redundancy — always reporting the original byte offset
of any problem.

The tool is a pure terminal program. It processes only local files and never
contacts the network.

## Build and test

```sh
go mod download
go build ./...
go test ./...
```

Requires Go 1.23+ (the module declares `go 1.23`). Dependencies are pinned:

- `github.com/abema/go-mp4 v1.4.1` — MP4/MOV container and track parsing
- `github.com/pion/rtp v1.8.27` — RTP header/sequence parsing

## Usage

Generate the four deterministic sample pairs:

```sh
go run ./cmd/mediasync demo --out /tmp/mediasync-fixtures
```

This writes `ok`, `wrap_drift`, `packet_loss` and `truncated` scenarios, each
an `.mp4` plus a framed `_packets.bin`, alongside an `ok`-pair alias named
`packets.bin`.

Validate one pair:

```sh
go run ./cmd/mediasync verify \
  --input /tmp/mediasync-fixtures/ok.mp4 \
  --chunks /tmp/mediasync-fixtures/packets.bin \
  --workers 4 --queue 8
```

Output is canonical JSON containing `sync_error_ms`, `raw_sync_error_ms`,
`drift_ppm`, `loss_recovered`, `loss_total`, `latency_p95_ms`, `error_code`,
`offset` and `content_hash`, followed by `recovery_rate`.

### Exit codes

| Code | Meaning                                                |
|------|--------------------------------------------------------|
| 0    | all checks passed                                      |
| 1    | runtime/operational error or cancellation              |
| 2    | media validation failure (a fault with a file offset)  |

## Architecture

Each subsystem has its own package and independent tests.

- `internal/fault` — unified error model. Every fault carries a stable numeric
  code, track name and original input byte offset.
- `internal/clock` — the shared time model. All time-base conversion uses
  exact `math/big` rationals; VFR marks use each sample's real duration; drift
  is fitted and calibrated while raw and corrected values are both retained;
  32-bit RTP timestamps are unwrapped, with ambiguous half-cycle wraps
  reported rather than guessed.
- `internal/container` — MP4/MOV parsing over go-mp4. Bounds every box against
  the file size, expands `stts`/`stsc`/`stsz`/`stco`/`co64` tables with
  overflow checks and resolves every sample offset against the `mdat` region.
- `internal/stream` — framed RTP parsing, sequence de-duplication/reordering,
  gap enumeration, timestamp unwrapping and single-packet XOR recovery.
  Unrecoverable holes are counted explicitly.
- `internal/pipeline` — bounded worker pool with hard caps on workers and
  queue depth, context cancellation that drains in-flight work without
  publishing successes, and input-index-stable result merging.
- `internal/report` — deterministic canonical serialization. Faults are sorted
  by `(offset, code, track, detail)` and the SHA-256 content hash covers only
  data-derived fields, so identical inputs always hash identically. Timing
  latency is overlaid for display only and never enters the hash.
- `internal/fixture` — code-generated sample data; no external media needed.
- `internal/verifier` — wires the subsystems together into the cross-track
  sync/drift/loss evaluation used by the CLI.
- `cmd/mediasync` — the `demo` and `verify` command-line entry point.

## Safety limits

Single-file size, track count, sample/table counts, packet count, frame size,
queue capacity and worker count are all capped. Offset arithmetic checks for
uint64 overflow and file/stream boundaries; nothing is silently skipped.
