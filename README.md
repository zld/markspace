# rtspdiag

A fully local, reproducible RTSP/RTP diagnostic tool for validating the
realtime path between a level-replay capture client and a local streaming
receiver. It drives an in-process mock RTSP server over either UDP or
RTP-over-RTSP/TCP and measures:

- RTSP session **state transitions** (OPTIONS → DESCRIBE → SETUP → PLAY →
  TEARDOWN), rejecting and counting illegal orders.
- RTP **sequence gaps, missing/duplicate/late packets** and reorder timeouts,
  with correct 16-bit sequence wraparound.
- **Frame reassembly** keyed by SSRC and RTP timestamp, closed by the marker
  bit, with unclosed frames reported as incomplete.
- RTP **parse errors** (truncated header, illegal padding, extension
  overrun, oversized datagram) that are counted and safely skipped.

No external services are contacted: every connection targets a mock server
bound to `127.0.0.1` on an ephemeral port, released on exit. All fixture data
is generated deterministically in code.

## Requirements

- Go 1.22+ (tested on macOS darwin/arm64 and darwin/amd64)
- Module dependencies managed with Go Modules

## Build and run

```sh
go mod tidy
go build -o /tmp/rtspdiag ./cmd/rtspdiag

/tmp/rtspdiag run --transport udp --seed 42 --window 8 --timeout 25ms --json
/tmp/rtspdiag run --transport tcp --seed 42 --window 8 --timeout 25ms --json
```

Text output (omit `--json`) renders the same fields as aligned key/value
lines.

### CLI flags

| Flag               | Meaning                                            |
|--------------------|----------------------------------------------------|
| `--transport`      | `udp` or `tcp` (RTP-over-TCP interleaved)          |
| `--seed`           | Deterministic fixture seed                         |
| `--window`         | Reorder window size, 2..4096                       |
| `--timeout`        | Reorder flush timeout, 1ms..10s (e.g. `25ms`)      |
| `--json`           | Emit a machine-readable JSON report                |
| `--max-packet-size`| Maximum accepted RTP datagram size, 64..65535      |

Invalid boundaries fail at startup before any session opens.

### Exit codes

- `0` — run completed and report emitted
- `1` — runtime / protocol processing failure
- `2` — invalid command-line parameters

## Architecture

```
config        flag parsing and boundary validation
fixture       deterministic RTP sequence generator (wrap, reorder,
               gap, duplicate, malformed)
sim            in-process gortsplib mock RTSP server (loopback only)
rtsp/session   project-owned RTSP state machine, shared by both transports
rtsp/transport TCP/interleaved adapter (gortsplib)
rtsp/transport_udp  raw RTSP signaling + self-managed UDP data plane
rtp/parser     own RTP fixed/extension/padding parser
rtp/reorder    bounded uint16-aware reorderer with gap and timeout logic
rtp/frame      SSRC/timestamp frame reassembler
rtp/pipeline   parser -> reorder -> frame wiring
report         JSON/text summary
cmd/rtspdiag   CLI entry point
```

### Why the UDP data plane is self-managed

gortsplib's client uses a hardcoded 64-deep UDP reorder buffer that retains
every packet following a sequence gap until the missing packet arrives. With
a real (never-filled) gap it hides the loss and stalls delivery, which defeats
the purpose of this diagnostic. The UDP adapter therefore uses gortsplib-style
raw RTSP signaling over TCP but reads RTP from its own UDP socket and feeds the
project-owned parser and reorderer directly, so gaps and timeout flushes are
measured accurately. The TCP path retains gortsplib interleaved transport
because TCP is reliable and ordered and never stalls on a network gap.

## Tests

```sh
go test ./...
```

Covers parsing (CSRC/extension/padding/malformed), the state machine happy
path and illegal transitions, sequence wraparound and reordering, gap and
timeout accounting, frame reassembly, fixture determinism, cleanup after
disconnect, and full UDP/TCP end-to-end runs.

## Demo

```sh
./scripts/demo.sh
```

Builds the binary, runs the test suite, executes both transports and
demonstrates the parameter-error exit code.
