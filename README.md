# Media Kernel (mediakernel)

An offline, dependency-light Go kernel for validating multilingual game
dialogue, subtitle and sound-effect deliveries when audio packet durations
change and packets are lost or duplicated. It builds a content/container
digest index, recomputes only the assets affected by a change, and emits
machine-readable JSON with verifiable specs and alignment results.

- Pure local execution, no external services or real media files.
- Test fixtures are generated deterministically from a seed.
- Fixed, test-pinned algorithms and formulas (documented below).

## Build and test

```sh
go mod tidy
go build ./...
go test ./...
./accept.sh           # end-to-end acceptance with documented exit codes
```

Go 1.23. Pinned third-party dependencies (see `go.mod`):

| Library          | Version | Use                                        |
|------------------|---------|--------------------------------------------|
| abema/go-mp4     | v1.2.0  | MP4 container wrap/parse (`mp4_container.go`) |
| pion/rtp         | v1.8.9  | RTP header marshal and sequence handling (`packet.go`) |

## CLI

```sh
go run ./cmd/assetcheck demo --seed 7 --queue-cap 8 --slow-consumer-ms 2 --manifest-version 1
go run ./cmd/assetcheck demo --seed 7 --corrupt truncated --expect-error
go run ./cmd/assetcheck demo --seed 7 --interrupt-migration after-stage
go run ./cmd/assetcheck verify --repeat 2 --print-json
```

Exit codes: `0` spec passed, `1` business validation failure, `2` argument or
runtime error (corrupt input, interrupted migration, bad flags).

> Note on `go run`: the Go tool reports a child process's non-zero exit as its
> own generic code `1`, so the precise `0/1/2` contract is observable from the
> compiled binary. Build first for exact codes:
> `go build -o assetcheck ./cmd/assetcheck && ./assetcheck ...`. The acceptance
> script `accept.sh` does this automatically. The `--expect-error` flag still
> causes a non-zero (failure) exit either way.

## Architecture and data flow

```
seeded fixture ─▶ MP4 wrap (go-mp4) ─▶ MP4 parse ─▶ mdat packet stream
   (fixture.go)     (mp4_container.go)                     │
                                                            ▼
        FrameReader (RTP, pion) ─▶ GapTracker ─▶ Timeline (shared Clock)
            (packet.go)            (gap_tracker.go)         │
                                                            ▼
              BoundedQueue (backpressure/cancel) ─▶ consumer (stream.go / pipeline.go)
                                                            │
                ┌───────────────────────┬──────────────────┴──────────────────┐
                ▼                       ▼                                      ▼
        ContentHasher          IncrementalIndex                    Metrics + Alignment
        (digest.go)            (incremental_index.go)               (metrics.go)
                └───────────────────────┴──────────────────┬──────────────────┘
                                                            ▼
                                              Migrator (validate→stage→commit)
                                                  (manifest.go / migrate.go)
```

### Subsystems and files

| File | Responsibility |
|------|----------------|
| `clock.go` | Unified media clock (48 kHz default), tick/duration/ms conversion, `Timeline` |
| `media_error.go` | Structured `MediaError` (offset, phase, seq, time range) and exit-code mapping |
| `packet.go` | Opus/AAC duration validation, length-prefixed RTP framing, offset-bearing parse errors |
| `gap_tracker.go` | Loss, duplicate and 16-bit sequence wraparound classification |
| `stream.go` | Generic bounded FIFO, blocking backpressure, cancellation, peak counters |
| `digest.go` | SHA-256 content/container digests and canonical-JSON hashing |
| `incremental_index.go` | Source/asset change classification and recompute/reuse planning |
| `metrics.go` | Bitrate deviation, SNR proxy, subtitle alignment and summaries |
| `manifest.go` / `migrate.go` | Versioned manifests, V1→V2 adaptation, staged atomic migration/rollback |
| `fixture.go` | Deterministic Opus/AAC streams with loss, duplication, wrap and corruption |
| `mp4_container.go` | go-mp4 encapsulation and parsing with located truncation errors |
| `pipeline.go` / `runner.go` | Streaming pipeline and end-to-end orchestration |
| `cmd/assetcheck/main.go` | Terminal entry point |

## Pinned algorithms and formulas

### Media clock (`clock.go`)

- Default rate **48000 ticks/second**, shared by dialogue, subtitle and SFX.
- `duration_ns = ticks * 1e9 / rate`; `ms = ticks * 1000 / rate`.
- Legal per-packet durations:
  - Opus samples: `{120, 240, 480, 960, 1920, 2880}`.
  - AAC samples: `{960, 1024}`.
- Duplicate packets never advance the clock. A lost packet reserves a gap
  sized by the codec's canonical frame (Opus 960, AAC 1024 samples); no audio
  content is synthesized.

### Digests (`digest.go`)

- Algorithm fixed to **SHA-256**; digests are `{"algorithm":"SHA-256","hex":...}`.
- **Content digest**: SHA-256 over each *unique* packet encoded as a fixed
  prefix (`codec`, `seq`, declared samples) + payload length + payload bytes.
  RTP/framing headers and duplicate packets are excluded, so identical payloads
  keep the same content digest across encapsulation changes.
- **Container digest**: SHA-256 over the complete container bytes (the full MP4
  document in the end-to-end path).
- **Canonical/index/manifest digest**: SHA-256 over compact JSON; map keys are
  sorted by `encoding/json`, and source states are sorted before hashing, so
  the digest is independent of input ordering.

### Incremental index (`incremental_index.go`)

For each source, compare content and container digests:

| Content | Container | Classification |
|---------|-----------|----------------|
| same    | same      | `unchanged`    |
| same    | differs   | `container-only` |
| differs | any       | `content`      |
| absent  | absent    | `new-source`   |

An asset is recomputed when a dependency is new or content-changed, or when a
container-only change affects an asset with `needs_container=true`. Otherwise
it is reused. Decisions are sorted by asset id for a deterministic affected set.

### Bitrate (`metrics.go`, `pipeline.go`)

- `effective_bps = total_payload_bytes * 8 / duration_seconds`.
- `relative_deviation = (actual - target) / target`; pass when
  `|deviation| <= tolerance`.
- Fixture delivery spec (documented constant): target **24000 bps**, tolerance
  **0.20** (`FixtureTargetBitrateBps`, `FixtureBitrateTolerance`).

### Quality SNR proxy (`metrics.go`)

Reproducible, timing-independent proxy for lossy processing:

```
snr_proxy_db = 10 * log10(signal_energy / noise_energy)
```

Reused/unprocessed assets and zero-noise (lossless) paths report
`applicable=false` with a machine-readable reason rather than a fabricated or
infinite score. The processed demo stream uses the fixed model 100/1 = **20 dB**.

### Subtitle alignment (`metrics.go`)

- `start_error_ms` / `end_error_ms` are signed differences between subtitle and
  audio interval edges on the shared clock.
- `abs_max_error_ms = max(|start|, |end|)`; pass when within tolerance (25 ms).
- Each item carries the source content digest for traceability; the summary
  reports max and average absolute error in milliseconds.

### Manifest versions and migration (`manifest.go`, `migrate.go`)

- **V1** (`LegacyManifestV1`): entries have `source_digest` only — no container
  digest and no quality block. `DecodeManifest` adapts V1 to the current V2,
  filling an empty container digest (forcing container rebuilds) and a
  not-applicable quality result.
- **V2 (current)**: adds `container_digest`, `quality`, `index_digest`,
  `manifest_digest` and migration `stage`.
- `manifest_digest` is the SHA-256 of the canonical document *excluding the
  digest field itself*.
- Migration stages: **validate → stage → commit**. The new document is written
  to a same-directory temp file; the current document is copied to a `.rollback`
  point; commit is an atomic `rename` (with directory fsync). An interrupt at
  any stage leaves the target byte-identical; `Restore()` reinstates the
  rollback point.

## Determinism and self-checks

- Fixtures are derived solely from `(seed, packet_count, corrupt)` — identical
  specs produce byte-identical streams.
- `verify --repeat N` re-runs the pipeline and asserts identical manifest
  digest, index digest and recompute/reuse asset sets on every repeat.
- Boundary cases (time mis-order, digest mismatch, interrupted migration,
  truncated/invalid/sequence-corrupt packets) are generated by code and pinned
  by the `*_test.go` suites.

## JSON output fields

Every result is JSON and includes at minimum: packet count, lost/duplicate
counts, queue peak and capacity, content/container/index digests, recompute and
reuse counts, target/actual bitrate and relative deviation, quality metric,
alignment errors in milliseconds, corrupt-packet records and migration stage.
