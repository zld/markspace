# media-check

A reusable, fully local media-processing kernel that turns a captured
RTP/RTCP packet stream into verifiable, multi-platform-ready resources and
supports crash-safe publishing with rollback.

All input streams are **self-generated fixtures** — no external media files
or network services are required.

## Requirements

- macOS (Darwin), Terminal
- Go 1.23.x (a newer local toolchain also builds the module; `go.mod` pins
  the language level to 1.23)
- Dependencies, pinned in `go.mod` / `go.sum`:
  - `github.com/abema/go-mp4 v1.3.0` — MP4 structure and sample parsing
  - `github.com/pion/rtp v1.8.11` — RTP packet parsing

## Setup, build, test

```bash
brew install go          # or use an existing Go toolchain
scripts/install.sh       # go mod tidy, pins dependencies
scripts/build.sh         # builds all packages + bin/media-check
scripts/test.sh          # go vet + full test suite (offline)
```

## Acceptance demo

```bash
go run ./cmd/media-check demo --seed 17 --loss 0,1,8 --reorder 3 \
  --wrap-seq 65534 --crash-after-artifact --rerun --rollback-to v1
# or simply:
scripts/demo.sh
```

### Flags

| Flag | Meaning |
| --- | --- |
| `--seed N` | deterministic fixture seed (same seed ⇒ identical digests) |
| `--loss a,b,c` | video media-unit indices dropped from the wire |
| `--reorder N` | move a packet N positions late to exercise reordering |
| `--wrap-seq S` | first RTP sequence number (e.g. 65534 forces 16-bit wrap) |
| `--crash-after-artifact` | write artifact bytes, then stop before ledger commit |
| `--rerun` | reopen the ledger and reconcile any orphaned artifact |
| `--rollback-to vN` | repoint current at a previously snapshotted version |
| `--workdir DIR` | ledger / derive-store / rollback working directory |

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | all assertions passed |
| 2 | input or time-base error (bad flags, ambiguous timestamp wrap) |
| 3 | recovery / ledger invariant failure (digest drift, lost committed artifact) |

## What the output contains

- track count, key-frame count, rational A/V sync error (max and P95, ms)
- damage segments: media-time range, first error byte offset, original
  extended RTP sequence range, fault codes
- loss recovery rate = rebuilt parseable units / recoverable missing units,
  clamped to `[0,1]`
- P95 processing latency and processed-unit count
- structure and damage content digests (identical for identical seeds)
- crash/rerun counters: reused, republished, missed-ledger, quarantined
- rollback version and manifest digest

## Architecture

```
fixture (generated RTP/RTCP + ground truth)
   │
   ├─ internal/rtp     extended-sequence reorder, dedup, loss gaps, wrap
   ├─ internal/rtcp    RTCP sender report → rational time-base mapping
   │
   ▼
internal/stream  structure, samples, key-frame intervals, A/V sync
internal/damage  time-anchored damage segments, recovery rate
   │             (both share internal/clock and internal/fault)
   ▼
internal/assets    immutable, SHA-256 content-addressed source assets
internal/derive    content-addressed derivatives; source change invalidates
internal/publish   two-phase recoverable ledger (write, then atomic commit)
internal/rollback  version manifests + auditable digest/transition trail
   ▲
internal/pipeline  orchestrates every stage above
cmd/media-check    terminal acceptance adapter
internal/mp4writer go-mp4 muxer producing Probe-readable avc1/mp4a tracks
```

### Key design properties

- **Exact time math.** Every media time is a `clock.Rational` fraction;
  floats are used only for human-readable output. Cross-track sync is
  computed rationally and rounded to milliseconds once.
- **No fabricated media.** Reorder gaps are recorded as loss; corrupt and
  truncated packets are faulted with their first error offset and skipped,
  while scanning continues over recoverable packets.
- **Unambiguous wrap flagged.** Sequence/timestamp jumps reaching half their
  modular space cannot be decided uniquely and raise a time-base fault.
- **Content addressing.** Sources are immutable; derivatives are keyed by
  their own digest and bound to a source digest, so a changed source simply
  misses the cache.
- **Crash recovery.** Artifact bytes are flushed before the ledger entry is
  atomically committed. A rerun verifies the orphan digest, re-adopts it once
  (reused = 1, republished = 0), or quarantines and regenerates on mismatch.
- **Auditable rollback.** Each version manifest pins source and derived
  digests; every snapshot/rollback appends a transition to an audit log.

## Package tests

Independent test files cover the three explicitly required areas plus the
rest of the kernel:

- `internal/rtp/reorder_test.go`, `leading_loss_test.go` — reorder/dedup/wrap
- `internal/stream/inspect_test.go` — structural analysis and sync
- `internal/publish/ledger_test.go` — crash recovery and invariants
- damage, derive, assets, rollback, rtcp, mp4writer and pipeline tests
