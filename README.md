# relaylab

A reproducible, loopback-only real-time media relay experimenter for a single
Mac. Two in-process Pion `PeerConnection`s negotiate over an HTTP signaling
server bound to `127.0.0.1`, exchange trickle ICE candidates restricted to
loopback addresses, and stream deterministic RTP audio through a bounded relay
queue with injected reordering, burst delay and duplication. A JSON report
summarises ICE candidate pairs, forwarding counters and RFC 3550 jitter.

No external services, public network, browser, camera or microphone are used.
Every socket (HTTP signaling and UDP ICE) is bound to `127.0.0.1`.

## Requirements

- macOS (Darwin), Go 1.22+ toolchain (`brew install go`)
- Dependencies: `github.com/pion/webrtc/v4 v4.0.0`, `github.com/pion/rtp v1.8.5`
  (the exact rtp version is enforced with a `replace` directive).

## Build and run

```sh
go mod download
go build -o /tmp/relaylab ./cmd/relaylab
/tmp/relaylab run --duration 5s --packets 1200 --queue 64 \
  --overflow drop --seed 42 --json
```

Build artifacts go to `/tmp`, never into the delivery tree.

### Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--duration` | `5s` | Experiment length, hard-capped at 30s |
| `--packets` | `1200` | Deterministic RTP packets to generate |
| `--queue` | `64` | Bounded relay capacity |
| `--overflow` | `drop` | Queue-full policy: `drop` or `block` |
| `--seed` | `42` | Deterministic fault-injection seed |
| `--reorder-rate` | `0.08` | Fraction of packets emitted out of order |
| `--duplicate-rate` | `0.05` | Fraction of packets duplicated |
| `--burst-every` | `120` | Burst-delay period in packets (0 disables) |
| `--burst-delay-ms` | `30` | Delay injected during each burst |
| `--shutdown-timeout` | `3s` | Grace period for clean teardown |

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Connection established, report complete, cleanup succeeded |
| 2 | Invalid arguments or signaling/negotiation error |
| 3 | Transport or timeout failure |
| 4 | Shutdown cleanup did not finish within the deadline |

## Architecture

```
cmd/relaylab          flag parsing, exit-code contract, JSON output
internal/signaling    loopback HTTP server, session FSM, monotonic seq
internal/peer         Pion PeerConnection, loopback-only ICE filters
internal/forward      RTP validation, bounded relay, fault injection
internal/stats        RFC 3550 jitter estimator, JSON report
internal/scenario     two-peer engine and deterministic traffic generator
scripts/accept.sh     build, race tests and acceptance-field verification
```

Data flow: the generator drives sender A's local track; packets cross the
loopback PeerConnection to receiver B, which enqueues them in the bounded
relay. The relay applies overflow policy and a packet-count-driven
backpressure schedule, then the sink feeds the jitter estimator. SSRC,
sequence number, timestamp and extension bytes are never rewritten.

## Tests and acceptance

```sh
go test -race ./...
./scripts/accept.sh
```

The acceptance script validates that `candidate_pairs` is in `[1,4]`, packet
counts have the expected magnitude, jitter is non-negative, `shutdown_clean`
is true, and the report contains no non-loopback address.
