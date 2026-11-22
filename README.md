# rtspcheck

Local, deterministic validation of RTSP/RTP session behavior between a
client and an in-process relay simulation. It compares the two RTP
transports — plain UDP and RTSP TCP-interleaved — against the same
state machine, RTP sequence checker and frame assembler, and reports
whether state transitions and frame boundaries survive packet loss,
reordering and the 16-bit sequence wraparound.

Everything runs on the loopback interface. There is no external
service and no flag to point at an arbitrary host; the RTSP server and
its RTP stream are generated in process.

## Build and run

```sh
go mod download
go test ./...
go test ./testdata        # testdata is importable but excluded from ./...
go run ./cmd/rtspcheck --transport both --scenario generated --json
```

Flags:

- `--transport udp|interleaved|both` (default `both`)
- `--scenario generated|wraparound` (default `generated`)
- `--json` emit machine-readable JSON (otherwise a short text summary)
- `--fail-method DESCRIBE|SETUP|PLAY|TEARDOWN` inject one failure
  (testing hook for the non-zero-exit paths)

Exit codes: `0` success and expectations met, `1` session failure or
statistics mismatch, `2` invalid parameters or scenario configuration.

## What the generated scenario contains

A single SSRC, three frames, one 65535 -> 0 wraparound, one
out-of-order-but-complete frame, and exactly one sequence gap:

| frame | timestamp | sequences | outcome |
|-------|-----------|-----------|---------|
| A | 1000 | 65534,65535,0,1 | complete, crosses the wraparound |
| B | 2000 | 3,2,4 (3 before 2) | complete, reordered then filled |
| C | 3000 | 5 (6 lost, declared as stream tail) | incomplete, one gap |

Both transports report 6 state transitions, 1 sequence gap, 2 complete
frames and 1 incomplete frame. The independent `wraparound` scenario is
the small sample 65534,65535,0,1 on one timestamp with the marker only
on seq 1: it reassembles into one complete frame with zero gaps.

## Layout

- `internal/session` — transport-agnostic RTSP state machine
  (Init…Failed) and transition rules.
- `internal/transport` — gortsplib-backed UDP and interleaved adapters
  sharing one runner, the state machine and the assembler.
- `internal/rtp` — modulo-65536 sequence checker (reorder tolerant,
  wraparound aware) and SSRC/timestamp/marker frame assembler.
- `internal/sim` — loopback-only gortsplib server emitting the
  deterministic packet stream, with optional method failure injection.
- `internal/report` — deterministic JSON statistics and expectation
  validation.
- `testdata` — code-generated deterministic scenarios.
- `cmd/rtspcheck` — terminal entry point.
