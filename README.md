# mediad

`mediad` turns timestamped multi-camera MP4/MOV recordings into an editor
hand-off list. It segments on sync frames, detects silence, fingerprints slice
content for de-duplication, checkpoints batch progress, and proves every
recorded hash against the media.

Everything runs locally. No network media, no device permissions, no
transcoding. Fixtures are generated in Go.

## Quick start

```sh
brew install go
go mod tidy && go build ./... && go test ./...
go run ./cmd/mediad demo   --out ./tmp/demo
go run ./cmd/mediad verify --dir ./tmp/demo
```

Exit codes: `0` all invariants passed, `1` damaged input with recoverable
output, `2` index or verification failure.

## What the demo does

1. Generates a deterministic corpus under `./tmp/demo/media`: two clean camera
   files, a duplicated feed, and four damaged files (truncated box, corrupt
   sample size, 32-bit timestamp wrap, missing sync frames).
2. Runs a continuous checkpointed batch, then an interrupted-then-resumed
   batch, and proves both manifests hash identically.
3. Writes `./tmp/demo/manifest.json` with per-segment SHA-256 values.
4. `verify` re-reads every segment's bytes, recomputes its hash, checks
   structural invariants, and replays the saved checkpoint.

Reported fields: `roundtrip_rate`, `keyframe_hit_rate`, `segments`,
`recovered_segments`, `duplicate_segments`, `segment_sha256`, plus
`continuous_manifest_sha256` / `resumed_manifest_sha256` /
`resume_identical`.

## Layout

| Package | Responsibility |
| --- | --- |
| `internal/clock` | Unified nanosecond timeline; decode/cto/edit-list mapping; wrap, regression and overflow detection |
| `internal/fault` | Located, recoverable errors; maps damage to exit codes |
| `internal/mp4` | Box walker (moov first/last, 64-bit sizes) and validated sample tables |
| `internal/index` | Keyframe index with exact vs inferred landings; repair from readable samples |
| `internal/audio` | PCM window RMS and RFC 6464 RTP audio-level silence; AAC zero-payload estimate |
| `internal/fingerprint` | Extent-scoped content hashes, collision review, manifest hashing |
| `internal/pipeline` | Segmentation, batch manager, session wiring |
| `internal/checkpoint` | Append-only length+CRC journal and atomic checkpoints |
| `internal/verify` | Cross-stage invariants and hash recomputation |
| `internal/fixtures` | Deterministic MP4/PCM/AAC writers and fault injection |
| `cmd/mediad` | Terminal entry point |

## Design notes

- **Damage is located, not swallowed.** Every fault carries file, offset,
  track and sample. Unreadable samples are dropped from results; the readable
  prefix is still delivered.
- **Recovery is a claim, not a guess.** Repaired index landings are marked
  inferred. `keyframe_hit_rate` counts only exact sync-frame cuts, so repair
  widens reachable range without inflating the metric.
- **Silence is never fabricated.** An absent or undecodable audio track is
  reported as undetectable. AAC silence is inferred only from all-zero
  spectral payloads, which can miss quiet audio but cannot invent silence.
- **Duplicates need evidence.** Segments merge on identical extents and
  content. Equal hashes over differing extents require per-sample review
  first; a hash alone never merges two slices.
- **Checkpoints are durable per block.** Journal appends are fsynced, then the
  checkpoint is published by rename. A torn tail is discarded and truncated so
  later records remain visible. Resume matches work by segment identity, not
  block index.

## Limits

- Single file ≤ 2 GiB (Go int64 offsets; guarded reads).
- Single segment ≤ 30 minutes, enforced in the planner and re-checked by
  `verify`.
- No transcoding and no decoding of compressed video/audio beyond AAC
  zero-payload inspection.
