# pwf — multi-camera ingest workflow

A terminal-only, fully local Go program that turns multi-camera capture
material (RTP Opus/AAC streams and MP4 audio tracks) into:

- per-track **conversion configurations** (passthrough / transcode / reject),
- a quantified **quality report** with measurements and the reason for every
  decision,
- a verifiable **provenance manifest** with byte offsets and SHA-256 hashes,
- resumable execution with checkpoints that survive interruption and detect
  tampered outputs.

It does **not** perform audio encoding. It delivers the conversion
configuration, reports and verification lists; no encoded audio is claimed or
produced.

## Build and run

```sh
brew install go@1.23
export PATH="$(brew --prefix go@1.23)/bin:$PATH"
go mod download && go mod tidy
go build -o /tmp/pwf ./cmd/pwf
go test ./...
/tmp/pwf verify --work /tmp/pwf-check --seed 41
```

`pion/rtp@v1.10.6` declares `go 1.24.0` in its own go.mod. The brew go1.23
binary therefore transparently switches to the go1.24 toolchain via Go's
toolchain rule; no network service is used at runtime. The project's go.mod
pins `go 1.24.0` and exact dependency versions.

### Expected verify output

```
damage_expected_offset=137
damage_found_offset=137
offset_error_bytes=0
first_blocker=plan
resume_ms=<small>
reused_nodes=2
sha256_full=<64 hex>
sha256_resume=<same 64 hex>
sha256_match=true
verify=ok
```

The small self-generated fixtures resume well inside 30 seconds.

## Commands

- `pwf verify --work <dir> --seed <int>` — generate healthy, damaged,
  infeasible-budget and interrupted fixtures and run every scenario.
- `pwf inspect --work <dir> --rtp <path> --codec opus|aac [--clock <hz>]`
  or `--mp4 <path> [--tight-budget]` — parse and plan one local source.

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | success / verification passed |
| 2 | argument or usage error (incl. path traversal) |
| 3 | material error: corrupt/truncated input, undetermined duration, bad length |
| 4 | task failure: infeasible plan, DAG/checkpoint failure, verify mismatch |

## Package layout and data flow

```
RTP/MP4 on disk
  -> internal/ingest      parse to offset-annotated records (streaming)
  -> internal/quality     measure gaps/anomaly/bitrate, plan per-track config
  -> internal/dag         stable topo order, bounded concurrency, atomic commit
  -> internal/checkpoint  node state + output SHA-256, tamper-aware reuse
  -> internal/provenance  source/offset/gap/node manifest, re-verifiable
  -> internal/pipeline    wires the stages, resumable
  -> cmd/pwf              terminal entry point
```

Shared foundations:

- `internal/media/time.go` — integer time base and duration arithmetic; all
  timing is exact integer ticks, never floating-point accumulation.
- `internal/media/error.go` — one error type carrying kind, stage, track,
  extended sequence number and exact byte offset. Nothing is skipped silently.

## Key behaviors

- **Opus duration** is computed from the TOC byte (RFC 6716), including the
  code-3 frame-count byte; **AAC duration** from the RFC 3640 AU headers or
  the MP4 sample (`stts`) time base. Undetermined duration is an error.
- RTP sequence numbers are extended to 64 bits; wraparound, reordering,
  duplicates and missing packets are handled. Duplicates are never timed
  twice; gaps are recorded and never filled with invented audio.
- Truncated MP4 boxes and damaged RTP headers/payloads are located to the
  exact byte offset.
- Planning enforces max-gap, anomaly-ratio, target-bitrate and processing-
  budget gates; an infeasible budget fails planning rather than dropping work.
- The DAG rejects cycles, runs in a stable topological order with bounded
  concurrency, reports the first blocking node, and writes outputs to temp
  files then atomically renames them.
- Node keys derive from input digest + config digest + a version constant. On
  rerun, recorded digests are verified against disk; a mismatch forces
  recompute, completed nodes are reused, and a resumed run converges to the
  same manifest digest as a full run.

## Bounds and safety

- RTP is read through a fixed 64 KiB streaming buffer; individual frames are
  capped (`MaxFrameBytes`), so a corrupt length cannot cause unbounded
  allocation.
- MP4 boxes are read on demand by offset and every declared extent is checked
  against the file size.
- Out-of-range lengths and path-traversal (`..`) inputs are rejected.
- Only local files are processed; there are no external service dependencies.
