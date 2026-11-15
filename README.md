# jbbench — jitter-buffer benchmark

A dependency-free Go benchmark for the receive-side jitter buffer of a
fixed-format realtime audio stream. It deterministically reproduces the
network conditions players see on Wi-Fi ↔ mobile handovers — reordering,
duplication, burst loss, jitter spikes and malformed datagrams — and verifies
that the **offline replay path and the live UDP path run the same buffer
control strategy**.

Standard library only (`net`, `encoding/binary`, `flag`, `testing`); no
third-party runtime dependencies and no external network access.

## Build and test

```sh
go test ./...
go run ./cmd/jbbench simulate --frames 12000 --seed 42 --loss-burst 7 \
    --reorder 12 --duplicate 0.03 --json
```

## Commands

| Command    | Purpose                                                        |
|------------|----------------------------------------------------------------|
| `simulate` | Generate an impaired scenario and replay it through the pipeline |
| `report`   | Same as `simulate` but always print the text summary            |
| `receive`  | Listen on loopback UDP and run the live receive path            |

Exit codes: `0` completed, `2` parameter error, `3` input/transport error,
`4` internal consistency failure.

Key flags: `--frames --seed --loss-burst --loss-rate --reorder --duplicate
--corrupt --window --evict-oldest --json`. The `receive` subcommand adds
`--addr --read-timeout-ms --max-idle-ms`.

## Architecture

```
simulator / UDP listener
        │  datagrams
        ▼
protocol.Decode ──► classified bad-packet counters (corrupt / version /
        │            length / ambiguous) — bad packets never touch playback
        ▼
jitter.ReorderWindow ── duplicate / late / overflow / reorder accounting
        │
        ▼
jitter.Controller ── target watermark from arrival-jitter EWMA, bounded by
        │             min/max levels, smoothing factor and max step
        ▼
jitter.Player (shared) ── drives window + controller + concealer on a clock
        │
        ▼
conceal.Concealer ── hold → attenuate → silence, crossfaded recovery
        │
        ▼
metrics.Collector ── watermark curve, drop taxonomy, concealment continuity,
                     latency quantiles; JSON or text report
```

- `internal/protocol`  fixed 24-byte header + 320-sample 16-bit PCM frame,
  FNV-1a checksum, modular uint16 sequence comparison. Exact half-interval
  distance (32768) is explicitly invalid.
- `internal/jitter`    bounded reorder window, watermark controller and the
  `Player` facade that both receive paths share.
- `internal/conceal`   packet loss concealment and recovery crossfade.
- `internal/transport` loopback UDP listener with timeout, truncation and
  race-safe close handling.
- `internal/receive`   `Replay` (logical per-event clock) and `RunLive`
  (real read deadlines), both built on the same `Player`.
- `internal/sim`       seeded, reproducible impairment generation.
- `internal/metrics`   report assembly, quantiles and consistency checks.
- `internal/testkit`   shared deterministic assertions.

## Report fields

`frames`, `received`, `played`, `concealed_frames`, `max_conceal_run`,
`max_recovery_jump`, `recovery_ratio`, latency `p50/p95/p99/max`,
`buffer_level_curve` (sampled to at most 2048 points), plus the independent
drop counters `corrupt_packets`, `bad_version_packets`, `bad_length_packets`,
`ambiguous_packets`, `duplicate_drops`, `late_drops`, `overflow_drops`,
`reordered_packets` and the aggregate `invalid_packets`.

Concealed frames are substitute output only; they never increment the network
reception counter. Scope is deliberately limited to fixed-format receive-buffer
validation — no codec, capture device or full voice product.
