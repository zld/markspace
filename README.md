# mediad — edge media segment diagnostics

`mediad` is a reusable Go library plus a small CLI for diagnosing live media
segments that are forwarded between edge nodes. Subtitles, timecodes, RTP
captures and MP4 fragments all arrive on their own clocks; node drift,
timestamp rollover, variable frame rate and truncated or out-of-order data
stack up on the same segment. The library normalizes every input onto one
exact rational master clock, indexes real keyframe intervals, localizes
damage to half-open byte ranges of the original input, and makes the result
reproducible and resumable.

## Running

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediad demo --seed 42 --checkpoint .tmp/checkpoint.json --report .tmp/report.jsonl
go run ./cmd/mediad verify --report .tmp/report.jsonl
```

`demo` generates its whole corpus in memory (no external fixtures) under a
temporary directory and prints one summary line:

```
cues=39 frames=450 keyframe_gaps=12 corrupt_ranges=8 offset_error_bytes=0 resume_ms=4 sha256=…
```

`verify` re-checks the recorded invariants and recomputes the checksum from the
report text alone:

```
invariants_failed=0 checks=54 gates_failed=0 sha256=… sha256_match=true records=646
```

### Exit codes

| code | meaning |
| ---- | ------- |
| 0 | run completed, report written, every invariant and stage gate passed |
| 1 | input damage detected (library contract, `demo --strict`) or a verification failure |
| 2 | bad arguments, rejected checkpoint, or an internal error |

The demo corpus intentionally contains damaged segments, so `demo` itself
returns 0 when the analysis succeeded; add `--strict` to get the library's
damage exit code. `verify` returns 1 when an invariant, a stage gate or the
checksum fails.

## Architecture

| package | responsibility |
| ------- | -------------- |
| `internal/core` | exact rational media clock, half-open byte spans, canonical SHA-256/JSON |
| `internal/ingest` | bounded asset loading, SRT/WebVTT, 24/25/29.97 DF/NDF timecodes, RTP, MP4 sample tables, drift sidecars |
| `internal/clock` | per-node drift fit, 32-bit rollover extension, master-timeline normalization |
| `internal/index` | real keyframe intervals from `stss`/`stts`, plus damage-aware trust flags |
| `internal/diagnose` | the offset-carrying error model and corrupt-range merging |
| `internal/recovery` | crash-safe checkpoint journal with input and stage digests |
| `internal/validate` | per-stage gates and cross-stage invariants |
| `internal/pipeline` | stage orchestration, JSON Lines report, verification |
| `internal/fixture` | deterministic in-memory corpus generation |
| `cmd/mediad` | `demo` and `verify` commands |

Data flows `ingest → normalize → index → diagnose → validate`. Each stage
commits a digest to the checkpoint; a resumed run starts at the first stage
not present in the journal and produces a report that is byte-identical to a
full run.

## Error model

Every finding carries the asset, format, pipeline stage, the fault category
and a half-open byte interval `[start, end)` in the *original* input. Ranges
never reference normalized coordinates. Recognized categories:

`truncated_box`, `mp4_length`, `srt_time`, `vtt_time`, `truncated_cue`,
`rtp_sequence_gap`, `rtp_wrap`, `clock_alignment`, `node_drift`,
`timecode_label`, `index_out_of_range`, `time_non_monotonic`, `invariant`,
`unsupported`.

A malformed record is always reported and skipped in a way that leaves the
following record intact: an illegal SRT/WebVTT cue does not consume the next
cue's text, and an impossible MP4 length field resynchronizes on the next
plausible box header rather than abandoning the tail.

## Determinism

* All clocks are reduced rationals, so equal values serialize identically.
* Report records are emitted in a fixed order and encoded as canonical JSON.
* The `sha256` in the summary covers every analysis record; the run header is
  provenance only, so full and resumed runs share one checksum.
* No wall-clock time, map iteration order or filesystem metadata reaches the
  report.

## Safety

* Asset reads are capped (`ingest.Limits.MaxAssetBytes`) before any parser
  trusts a declared length.
* Sample expansion is bounded so a corrupt `stts` cannot exhaust memory.
* Path traversal, absolute paths and symlinks escaping the stream root are
  rejected; report and checkpoint writes are confined to the output root.
* Checkpoints are length-capped, schema-versioned and verified by digest; a
  mismatch is refused rather than resumed.
