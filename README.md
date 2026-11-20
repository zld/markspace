# rtsp-ingest

A local-only H.264-over-RTSP reference-stream verifier. The tool starts its
own loopback RTSP server and a deterministic publisher, runs the full
`ANNOUNCE → SETUP → RECORD → TEARDOWN` flow over **UDP** and/or **TCP
interleaved** transport, and prints the control-plane state transitions, RTP
sequence-gap accounting and H.264 frame reassembly statistics as one stable
JSON line per transport.

Everything runs against `127.0.0.1` on ephemeral ports. There are no external
service dependencies and no external URLs are ever contacted.

## Requirements

- Go 1.22 (developed and accepted against the 1.22 directive)
- `github.com/bluenviron/gortsplib/v4` v4.12.2
- `github.com/pion/rtp` v1.8.11

All third-party source is committed under `vendor/`.

## Build and run

```sh
make vendor            # go mod vendor + restore the documented gortsplib patch
go test -mod=vendor ./...
go build -mod=vendor -o /tmp/rtsp-ingest ./cmd/rtsp-ingest
/tmp/rtsp-ingest demo --transport both --seed 17 --json
```

`--transport` is one of `udp`, `tcp`, `both` (default `both`). Output line
order is always UDP then TCP, and identical arguments produce byte-identical
output across runs.

## Output

Each JSON line contains, among other counters:

| field | meaning |
| --- | --- |
| `state_transitions` | real control-plane state changes |
| `rtp_seq_gaps` | distinct sequence numbers never received (each counted once) |
| `duplicate_packets` | packets whose sequence number was already seen |
| `reassembled_frames` | complete, valid H.264 access units |
| `discarded_frames` | access units dropped for missing/invalid fragments or oversize |

The deterministic fixture produces, on every transport:
`state_transitions=5`, `rtp_seq_gaps=1`, `reassembled_frames=2`,
`discarded_frames=1`, `duplicate_packets=1`.

## Exit codes

| code | meaning |
| --- | --- |
| 0 | every requested transport passed |
| 2 | invalid command-line arguments |
| 3 | RTSP protocol or state-machine failure |
| 4 | media data invalid (counter mismatch) |
| 5 | timeout, cancellation or internal error |

## Architecture

```
cmd/rtsp-ingest        CLI entry point, orchestration, exit codes
internal/cli           argument parsing and loopback-only target policy
internal/fsm           shared control-plane state machine (both transports)
internal/server        gortsplib callbacks, SDP validation, loopback runner
internal/transport     UDP and TCP-interleaved adapters over one Pipeline
internal/rtp           modulo-65536 reorder buffer + H.264 (NAL/STAP-A/FU-A)
internal/sim           deterministic fixture generator and RTSP publisher
internal/report        stable single-line JSON
```

Data flows: CLI → local simulator publisher → gortsplib server callbacks →
transport adapter → shared FSM / reorder buffer / H.264 reassembler → report.

### RTP reordering

The 64-packet reorder buffer sorts by sequence number modulo 65536, handles
wraparound, duplicates, out-of-window late packets and timeout flushes. A
hole that is later filled is refunded and never counted; a hole that survives
a flush is counted exactly once.

### H.264 reassembly

Single NAL, STAP-A and FU-A packets are reassembled per `(SSRC, payload type,
timestamp)` key. FU-A enforces start/end markers and fragment sequence
continuity. Frames are capped at **8 MiB**; oversize, incomplete or illegal
units are discarded and counted.

### Vendored gortsplib patch

gortsplib's server-side UDP path normally reorders packets and silently drops
duplicates before the application callback, which would make application-level
gap/duplicate accounting impossible. The vendored
`server_session_format.go` is patched to bypass that queue so packets arrive in
raw order (details in `docs/VENDOR_PATCH.md`).

A bare `go mod vendor` refreshes `vendor/` from the pristine module and wipes
this hunk. Use the wrapper target, which vendors and immediately restores it:

```sh
make vendor
```

or re-apply it manually with `go run -mod=vendor ./tools/apply-vendor-patch`
(the tool is idempotent). If the patch is ever missing, the
`TestVendorPatchPresent` integration test fails with the same remediation
message rather than letting counters silently go wrong.
