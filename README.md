# mediad — deterministic media transport kernel

A local, dependency-light Go kernel that ingests framed RTP/RTCP byte
streams from edge nodes and unifies parsing, jitter buffering, loss
recovery, A/V synchronization and keyframe indexing on **one event-time
timeline**. It is built to be *re-run*: identical parameters and seed
always produce identical event order, counters and a `result_sha256`
digest.

No network, device capture or GUI is involved. All sample data is
generated in-process.

## Requirements

- Go 1.23+
- Only two direct modules: `github.com/pion/rtp v1.8.11` and
  `github.com/abema/go-mp4 v1.4.1`. All other modules are their transitive
  dependencies, fetched by Go Modules.

## Build, test, run

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediad demo --seed 42 --loss 0.05 --reorder-ms 80 \
  --late-window-ms 250 --report-every 100
```

The demo prints JSON lines (`progress` events) and ends with one summary
line. Run it twice with the same arguments; the `result_sha256` must match.

Run every declared scenario:

```sh
go run ./cmd/mediad fixtures --seed 42
```

## Exit codes

| code | meaning |
|------|---------|
| 0 | demo/fixtures ran and the acceptance assertions passed |
| 1 | ran fine, but a metric missed its target or a fault scenario did not show its required signal |
| 2 | bad flag, out-of-range parameter, or unknown fixture |

`go test` failures are reported by Go itself with a non-zero status.

## Acceptance targets

For the **normal** sample (default demo flags):

- `av_sync_error_ms <= 40`
- `recovery_rate >= 0.80`
- `p95_latency_ms <= 250`
- `malformed_packets == 0`

Fault scenarios are checked for the signal they exist to prove (see
`cmd/mediad/main.go:checkFixture`):

- **corrupt** — nonzero `malformed_packets`, and the damage is visible in
  both `buffer_malformed` and `sync_malformed`; later good packets still
  emit.
- **truncated** — nonzero `truncated_tail_packets`.
- **late-rtcp** — `late_replayed > 0`, `late_feedback > 0`, reproducible.
- **no-sr** — `av_sync_confidence == "low"`, bounded error.
- **wrap** — the ambiguous rollover packet is quarantined.
- **clock-drift** — a nonzero `clock_drift_ppm`, still high confidence.

## Data flow

```
bytes ──▶ rtp.Parser ──▶ sequence.Extender ──▶ buffer.Jitter
                                    │                 │
                          RTCP NACK ─┐          watermark advance
                                    ▼                 ▼
                            recovery.Cache ──▶ timeline.Replay ──▶ sync.Estimator
                                                              └──▶ index.Rebuild
```

- **`internal/clock`** — one reference clock (ms) for RTP, sampling and
  MP4 timescales.
- **`internal/model`** — the `Event` currency and the error taxonomy.
- **`internal/rtp`** — length-framed incremental parser (RFC 4571 style)
  and 16→64-bit sequence extender.
- **`internal/buffer`** — event-time jitter buffer.
- **`internal/recovery`** — bounded retransmit cache answering NACKs;
  never invents media.
- **`internal/timeline`** — monotone watermark, late window, and
  deterministic replay.
- **`internal/sync`** — SR-anchored clock mapping, node-bias and drift
  estimation, bounded no-SR fallback.
- **`internal/index`** — GOP/sync-sample rebuild materialized as an MP4
  `stss` box.
- **`internal/pipeline`** — fixture generation and orchestration.

## Fault fixtures

`pipeline.Fixtures` declares seven scenarios generated from a seed: loss
and reorder, 16-bit sequence wrap, truncated tail, corrupt extension and
framing, late RTCP feedback, missing Sender Reports, and sender clock
drift. The network model drops, duplicates, reorders and delays media;
RTCP keeps a fixed one-way delay so NTP-vs-arrival comparison measures
clock drift rather than jitter.

The sender keeps a bounded retransmit cache populated from its
transmission log; recovery rate is scored against exactly the wire keys
the network dropped, so a cache miss is visible as a failure rather than
being hidden by fabrication.

## Summary fields

All summary JSON keys are stable; the digest is the SHA-256 of the
canonical JSON with `result_sha256` blanked.

| field | meaning |
|-------|---------|
| `av_sync_error_ms` | magnitude of the estimated A/V presentation offset |
| `recovered_packets` / `recovery_rate` | restored dropped packets and their fraction |
| `cache_miss` / `dup_nack` / `late_feedback` | recovery degradation counters |
| `late_replayed` / `late_dropped` | events re-inserted inside / beyond the late window |
| `malformed_packets` / `buffer_malformed` / `sync_malformed` | parse damage and its propagation |
| `p95_latency_ms` | nearest-rank 95th percentile release latency |
| `index_rebuilt` / `index_complete` | sync samples rebuilt and whether all GOPs are recoverable |
| `result_sha256` | deterministic digest of the run |

## Scope and limits

- Only the declared payload types `96` (video, keyframe flag in payload
  bit 0) and `97` (audio) are accepted; RTCP types 200/201/205 are parsed.
- Caches, the late window, and the duplicate/reorder horizons are all
  bounded. Exceeding any bound yields a counted degradation, never an
  unbounded allocation.
- This is a kernel and acceptance harness, not a production SFU: it does
  not decode media, encrypt, or persist to disk.
