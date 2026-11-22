# Media Recovery Kernel

A reusable, fully local Go media-processing kernel for recovering cuttable,
auditable historical assets from long-running recordings. It ingests material
manifests with sparse keyframes, edit lists and non-zero start timestamps,
recovers RTP streams damaged by duplication, reordering, wraparound and burst
loss, and produces segmentation, silence and content-fingerprint evidence on a
single unified timeline.

No network access and no external media samples are required: every MP4, RTP
capture and manifest is generated in-process from a seed.

## Requirements

- Go 1.23 (also builds on newer local toolchains; `go.mod` pins `go 1.23`)
- Dependencies fixed in `go.mod`:
  - `github.com/abema/go-mp4 v1.3.0` for MP4 box parsing/writing
  - `github.com/pion/rtp v1.8.9` for the RTP packet model and sequence handling

## Build, test and demo

```sh
go mod tidy
go build ./...
go test ./...

# Healthy material
go run ./cmd/archive demo --seed 73 --duration 1800s --loss-burst 14 \
  --reorder-window 32 --target-bitrate 2400000 --out /tmp/archive-report.json

# Corrupted index (digest mismatch forces a rebuild)
go run ./cmd/archive demo --seed 73 --corrupt-index --out /tmp/archive-corrupt.json

# Resume from the last completed segment
go run ./cmd/archive demo --seed 73 --resume --out /tmp/archive-resume.json
```

### Exit codes

| Code | Meaning                                            |
|------|----------------------------------------------------|
| 0    | completed, all invariants passed                   |
| 1    | internal error / invariant violation               |
| 2    | invalid input or manifest, or safety limit hit     |
| 3    | unrecoverable corruption                           |

## Subsystems

| Package        | Responsibility                                                                 |
|----------------|--------------------------------------------------------------------------------|
| `timeline`     | Unified microsecond clock, half-away-from-zero rounding, error taxonomy, edit-list/non-zero-start axis mapping |
| `manifest`     | Size-limited, path-safe input reads and atomic result writes                    |
| `rtprecover`   | Bounded wrap-safe reorder window, burst-loss gap detection, EWMA bandwidth and output-budget planning |
| `media`        | MP4 scan/index with keyframe fallback, timeline silence detection, tolerant fingerprints with provenance |
| `pipeline`     | Staged orchestration, index invalidation, checkpoint resume, quality statistics |
| `testfixture`  | Deterministic MP4/RTP generators and seed-driven transport damage               |
| `cmd/archive`  | Terminal demo entry point with invariant gates                                  |

## Key invariants

- All segments, silence intervals and fingerprints reference one unified
  microsecond timeline; raw timescales and RTP clocks are converted only at
  subsystem boundaries.
- Sparse-keyframe cuts snap to the nearest decodable boundary and report the
  signed boundary deviation; healthy material aligns within 40 ms.
- Duplicate RTP packets are never re-emitted; out-of-window late packets are
  counted and discarded; uint16 sequence wraparound is not treated as a new
  stream. A persistent hole slides the jitter buffer forward rather than
  poisoning later packets.
- Confirmed loss gaps carry start/end time and missing count, and are never
  crossed when segmenting, classifying silence or merging fingerprints.
- An input-digest change unconditionally invalidates the cached index and
  triggers a rescan. Repeated runs are deterministic: identical inputs yield
  identical digests, segment counts and statistics.

## Fault-class test coverage

- duplicate packets (`rtprecover/window_test.go`)
- reordering and sequence wraparound (`rtprecover/window_test.go`, `loss_test.go`)
- burst loss crossing silence detection (`media/silence_test.go`)
- edit-list offset across manifest alignment (`timeline/editlist_test.go`)
- digest change invalidating the index (`pipeline/run_test.go`)
- crash recovery and repeat-run determinism (`pipeline/run_test.go`)
