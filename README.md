# avbatch

Offline archival pipeline for captured mobile sessions. A capture group is a
CMAF/fMP4 initialization segment plus a run of fragments, with interleaved PCM
audio and the RTP packets that carried it. The tool places every sample on one
drift-corrected timeline, cuts it into a non-overlapping manifest, reports
silence, de-duplicates content, and writes a checksummed local index that can
be resumed after an interruption.

Everything is generated locally from a seed; the tool never opens a network
connection and never reads external media.

## Requirements

- Go 1.23 (`go.mod` pins `go 1.23.0`)
- `github.com/abema/go-mp4 v1.4.1`, `github.com/pion/rtp v1.8.9`

```
go mod tidy
go build ./...
go test ./...
```

## Acceptance run

```
go run ./cmd/avbatch demo --generate --seed 41 --block-samples 128 \
  --inject-seq-gap --inject-tfdt-regression --inject-wrap --inject-vfr \
  --inject-truncated-box --inject-checkpoint-crash
```

and the determinism check:

```
go run ./cmd/avbatch demo --generate --seed 41 --verify-repeat
```

`go run` collapses every non-zero exit status to 1. To observe the real exit
codes, build the binary first:

```
go build -o avbatch ./cmd/avbatch
./avbatch demo --generate --seed 41 --inject-truncated-box; echo $?   # 2
```

Exit codes: `0` complete, `2` usable output recovered from damaged input,
anything else failed.

## Output fields

| field | meaning |
| --- | --- |
| `segments` | media segments in the manifest (gaps excluded) |
| `silence_ms` | total detected silence on the audio track |
| `duplicates` | re-delivered fragments plus content-identical segments |
| `target_bitrate_kbps` / `actual_bitrate_kbps` | requested rate vs. delivered media rate |
| `bitrate_deviation_pct` | shortfall of delivered vs. requested |
| `quality_samples` | video samples that reached the manifest |
| `manifest_alignment_error_ms` | worst distance between a cut boundary and its nominal target |
| `recovered_blocks` | blocks in the produced index |
| `reused_blocks` | of those, blocks verified from an earlier attempt |
| `resume_offset` | byte offset of the first uncommitted entry after recovery |
| `manifest_sha256` | digest of the ordered manifest entries |
| `rtp_packets` / `rtp_wraps` / `rtp_anomalies` | RTP delivery, wrap crossings, backwards steps |
| `faults` | damage classes that made the run recoverable |
| `notes` | expected observations (re-delivery, repairs) that are not damage |

## Design notes

- **One timebase.** `clock.Timebase` converts ticks with an overflow-checked
  div/mod split. `clock.Timeline` fits an affine drift model against capture
  timestamps, reports placement uncertainty including projection distance, and
  refuses to order samples that occupy the same media tick. RTP timestamps are
  expanded with the signed modular delta, so forward and backward wrap are
  exact and a backwards step is reported, never rewritten.
- **No silent swallowing.** Parsing, ordering, clock, cutting, audio and index
  failures all carry a stable `mediaerr.Kind` plus segment ordinal, box type
  and byte offset. Damaged input still yields a report and verified blocks, but
  the index is not published; only verified blocks are ever reused.
- **Repair before estimation.** A tfdt regression is repaired by shifting that
  fragment and the whole tail by the accumulated offset before the drift fit
  runs. A shifted timestamp is a bogus observation, so using it would tilt the
  slope and corrupt every later correction.
- **Boundaries on real samples.** Every cut lands on a sample start, so VFR
  content is never split mid-frame. Keyframe preference is bounded to 20 ms of
  extra alignment error; intervals with no media become explicit gaps.
- **Dedup is verified.** A fingerprint hit only nominates a candidate.
  Duration, then a sampled digest, then the full content digest must agree;
  any failure keeps both copies and reports a collision.
- **Atomic index.** Blocks carry a sequence number, a CRC over the prefix and a
  sha256 over the payload. Blocks are fsynced as they close and the file is
  renamed into place at commit, so a crash can only leave a stale tail.

## Layout

```
internal/mediaerr   shared error model (kind, segment, box, offset)
internal/clock      timebase, drift model, RTP unwrap
internal/cmaf       boundary-checked fMP4 parsing
internal/segment    VFR-aware cutting with gaps and provenance
internal/audio      windowed multichannel silence detection
internal/fingerprint content digests, collision recheck, dedup
internal/index      checksummed block checkpoints and resume
internal/pipeline   end-to-end run
internal/synth      deterministic CMAF/PCM/RTP fixture generator
cmd/avbatch         terminal CLI
```

Performance on a single core for a synthetic 10-minute 720p/30 VFR capture:
about 0.2 s of pipeline time and under 60 MiB allocated, well inside the
90 s / 512 MiB target.
