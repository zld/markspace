# mediad — bounded-memory MPEG-TS inspection and re-packaging kernel

`mediad` is a reusable, dependency-light Go kernel for broadcast-grade batch
inspection and fragmented-MP4 re-packaging of MPEG-TS content. It locates
input corruption with byte offsets, unwraps PCR wraparound onto one monotonic
media timeline, aligns multiple bitrate representations onto a shared segment
grid, and supports interruption/resume with bit-identical output.

## Requirements

- Go 1.23 (the `go` directive in `go.mod` is pinned to `1.23`)
- macOS, fully local, no external services

Third-party dependencies are version-pinned in `go.mod`:

- `github.com/abema/go-mp4 v1.2.0` — fragmented MP4 muxing
- `github.com/pion/rtp v1.8.11` — RTP timestamp/sequence parsing

## Layout

```
cmd/mediad          acceptance CLI (demo / resume / verify)
internal/model      shared errors, corruption types, 27 MHz time base
internal/ts         188-byte reader, continuity tracker, PAT/PMT versions
internal/clock      PCR wraparound unwrap, RTP clock reuse
internal/flow       fixed-capacity queue with backpressure
internal/segment    streaming cutter, representation alignment, fMP4 muxer, manifest
internal/dag        dependency scheduler and durable checkpoint ledger
internal/fixture    in-code TS generator with five corruption modes
internal/engine     end-to-end pipeline wiring
internal/verify     manifest and fragment re-checking
scripts             reproducible acceptance runner
```

## Build and test

```sh
go mod download
go build ./...
go test ./...
```

## Commands

```sh
# Clean run: synthetic multi-bitrate TS -> fragments + manifest
go run ./cmd/mediad demo --out /tmp/mediad-out

# Corrupt run (sync | truncated | continuity | pcr | psi), exits 1
go run ./cmd/mediad demo --corrupt sync --out /tmp/mediad-bad

# Interrupt after two segments are durably committed
go run ./cmd/mediad demo --interrupt-after segment:2 --out /tmp/mediad-resume

# Resume from the latest valid checkpoint; manifest SHA matches a full run
go run ./cmd/mediad resume --out /tmp/mediad-resume

# Re-verify fragment sizes/digests and the manifest self-digest, exits 0
go run ./cmd/mediad verify --out /tmp/mediad-out
```

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | success |
| 1 | media or dependency failure (corruption located, checksum mismatch) |
| 2 | usage error (bad flags/arguments) |

## Output fields

- `segment_count` — distinct segment indices produced
- `representation_count` — bitrate tiers (always >= 2)
- `max_alignment_delta_ticks` — largest cross-representation boundary drift,
  bounded to one input packet duration
- `manifest_sha256` — canonical self-describing manifest digest
- `corruption_offset` / `affected_pid` — first located defect
- `resumed_from_segment` / `resume_ms` — resume origin and resume wall time

## Design guarantees

- **Bounded memory**: a fixed 32-packet queue and one open segment per
  representation; only running SHA-256 state is retained, never full segments.
- **Backpressure**: a slow consumer blocks producers instead of buffering;
  cancellation and writer failure release blocked producers.
- **No fabricated media**: every defect is reported with its absolute byte
  offset; truncated tails and bad sync bytes never produce invented samples.
- **Idempotent resume**: segments commit through a stable `rep:index` ledger
  with temp-file + atomic rename, so retries never double-submit and a re-run
  yields an identical manifest and SHA-256.
