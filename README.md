# rtspcheck

Local, loopback-only RTSP input validation tool for level-preview and
recording pipelines. It connects to an in-process mock camera source,
performs the RTSP request/response handshake, receives RTP over
interleaved TCP or UDP, reorders packets by sequence number,
reassembles frames and emits a deterministic report. Nothing binds
outside `127.0.0.1`; there is no external service, device or network
dependency.

## Build and requirements

- Go 1.22 toolchain (the module's `go` directive is pinned to `1.22`).
- All dependencies are vendored through Go Modules and version-pinned.
  gortsplib is pinned to `v4.13.1`, the last full release whose `go`
  directive (`1.21`) is compatible with the Go 1.22 toolchain; later
  tags require Go 1.23 and the final `v4.16.x` tag is an empty
  deprecation stub.

```sh
go mod download
go test ./...
go run ./cmd/rtspcheck --mode tcp --frames 120 --seed 41 \
  --drop-every 19 --reorder-window 8 --format json
```

If your default toolchain auto-upgrades, prefix Go commands with
`GOTOOLCHAIN=local`.

## Architecture

```
mock source (seeded fault schedule)
        │  gortsplib session, 127.0.0.1 only
        ▼
transport adapter (tcp.go interleaved | udp.go port allocator)
        │  raw serialized RTP datagrams
        ▼
rtp.Packet parser ──► rtp.Reorderer ──► frame.Assembler ──► report
        ▲                  ▲                   ▲
   malformed count    gaps/duplicates     complete/incomplete
```

- `internal/session/state.go` — one state machine
  (`Idle → Connecting → Described → Setup → Playing → Stopping →
  Closed`, plus `Failed`) with a single transition rule table shared by
  both transports. Illegal transitions return a locatable error.
- `internal/session/runner.go` — drives OPTIONS, DESCRIBE, SETUP, PLAY
  and TEARDOWN; failures are attributed to a phase and resources are
  always released.
- `internal/transport/` — TCP interleaved and UDP adapters. The UDP
  allocator binds loopback-only even/odd RTP/RTCP pairs inside a
  configurable range and returns `ErrPortExhausted` when full.
- `internal/rtp/` — bounded packet parser (version, CSRC, extension and
  padding checks; malformed input never panics) and a windowed reorderer
  handling duplicates, late packets, gaps and 16-bit wraparound.
- `internal/frame/` — reassembles by SSRC + RTP timestamp. Only a marker
  completes a frame; timestamp switches, session flush and buffer caps
  produce explicitly incomplete frames.
- `internal/mock/` — deterministic packet generator (seeded drops,
  duplicates, adjacent-swap reorder and jitter) plus a loopback
  gortsplib server with optional stage failures and connection drops.
- `internal/report/` — byte-stable JSON and single-line summary output.

## Command-line flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--mode` | `tcp` | `tcp` (interleaved) or `udp` |
| `--frames` | `120` | frames to generate and validate |
| `--seed` | `1` | deterministic RNG seed |
| `--drop-every` | `0` | drop 1 packet out of N (0 = off) |
| `--duplicate-every` | `0` | duplicate 1 packet out of N |
| `--reorder-prob` | `0` | adjacent-packet swap probability 0..1 |
| `--delay` | `0` | max per-packet jitter (microseconds) |
| `--reorder-window` | `8` | RTP sequence window (1..4096) |
| `--format` | `json` | `json` or `summary` |
| `--udp-min-port` / `--udp-max-port` | `30000` / `30200` | client UDP range |
| `--fail-stage` | — | inject a non-2xx response at `describe`/`setup`/`play` |
| `--drop-conn` | `false` | close the connection immediately after PLAY |
| `--max-frame-packets` / `--max-buffered-frames` | `512` / `256` | buffer caps |

## Report fields

`mode`, `state_transitions`, `illegal_rejections`, `received_packets`,
`sequence_gaps`, `duplicate_packets`, `late_packets`, `malformed_packets`,
`complete_frames`, `incomplete_frames`, `evicted_frames`, `final_state`,
`seed`, `frames_requested`.

## Exit codes

- `0` — session completed and the report was written.
- `1` — negotiation, transport or processing failure (including UDP port
  exhaustion and injected stage failures).
- `2` — invalid command-line arguments.

## Acceptance

```sh
scripts/acceptance.sh
```

The script builds the binary, runs the full unit-test suite and asserts
both transport modes end to end: required positive fields, complete
frames below the requested count under packet loss, identical state
transition counts across modes, a 120-frame/zero-gap no-fault baseline,
deterministic re-runs and the exit-code contract.
