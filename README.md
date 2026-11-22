# mediapipe — edge live-media transport recovery

A self-contained Go library and terminal adapter for recovering live media that
crosses lossy edge links: RTP jitter/reordering, sequence-gap recovery, a unified
integer timebase, strict fragmented-MP4 parsing, timed-metadata normalization,
cross-track alignment, and a verified content-addressed cache with resumable
checkpoints.

Everything is generated in-process from a seed. There are no external media
files, no network access, and no services.

## Quick start

```sh
go mod tidy
go build ./...
go test ./...
go run ./cmd/mediapipe demo --seed 17 --jitter-ms 80 --loss-burst 3 --checkpoint-dir ./tmp/checkpoints
```

Exit codes: `0` all invariants hold, `1` processing/verification failure,
`2` invalid arguments.

Useful variations:

```sh
# verify the cache is reused on a warm run (segment_cache_hits=6, cache_hits=24:
# each segment has independent transport, container, sync and index entries)
go run ./cmd/mediapipe demo --checkpoint-dir ./tmp/checkpoints

# simulate a crash after three committed segments, then resume the rest
go run ./cmd/mediapipe demo --checkpoint-dir ./tmp/checkpoints --interrupt-after 3
go run ./cmd/mediapipe demo --checkpoint-dir ./tmp/checkpoints --resume

# force unrecoverable loss: every burst retransmission misses the deadline
go run ./cmd/mediapipe demo --checkpoint-dir ./tmp/checkpoints --late-ratio 1000 --loss-burst 4
```

A normal run is not a resume, so it resets the checkpoint journal; pass
`--resume` to continue a previous run instead.

## Layout

| Path | Role |
| --- | --- |
| `internal/clock/timeline.go` | unified 90 kHz timebase, exact rational conversion, RTP wrap folding, drift |
| `internal/rtp/reorder.go` | jitter buffer with playout hold, wrap-aware sequencing, duplicate/late accounting |
| `internal/rtp/recover.go` | bounded retransmission policy and explicit missing-state records |
| `internal/rtp/payload.go`, `wire.go` | restricted fragment payload and RFC 3550 wire profile (pion/rtp) |
| `internal/mp4/parse.go` | strict fragmented-MP4 walk: truncation, length, offset, depth and allocation limits |
| `internal/mp4/index.go`, `write.go` | keyframe index with per-segment SHA-256 and a deterministic fixture writer |
| `internal/meta/timed.go`, `id3.go`, `codec.go` | canonical records for subtitles, chapters, ID3v2.4, emsg and SEI |
| `internal/sync/align.go`, `layout.go` | cross-track alignment with propagated uncertainty; layout diff diagnostics |
| `internal/cache/address.go` | content-addressed cache with provenance, version and content verification plus quarantine |
| `internal/pipeline/checkpoint.go` | append-only, self-checked journal with torn-tail tolerance and crash resume |
| `internal/pipeline/runner.go`, `stages.go` | stage orchestration and the four per-stage verified cache entries |
| `internal/pipeline/assemble.go`, `duration.go`, `faults.go` | fragment reassembly, declared-duration checks, fault matrix |
| `internal/fixtures/` | deterministic scenario generation and edge-network fault simulation |
| `cmd/mediapipe/` | flag parsing, exit codes and summary output only |

Every segment produces four independent cache entries — transport, container,
sync and index — each addressed by the digest of its own inputs and the
processing version, and each verified on read by stage, provenance, version and
content digest.

The adapter in `cmd/mediapipe/main.go` contains no media logic.

## Invariants

Every stage checks its own guarantees before a result becomes a commit point:

* transport — sequence continuity at emission, no duplicate emission, monotone
  media time per track, and explicit gap records instead of fabricated samples;
* container — every sample payload hashes to its declared digest, track ownership
  is declared in `moov`, and truncated or out-of-range boxes are rejected;
* sync — composition times are monotone, alignment reports its accumulated
  rounding/uncertainty bound, and layout differences become locatable diagnostics
  without rewriting source values;
* cache/checkpoint — an entry is trusted only when its key, input provenance,
  stage, version and stored content digest all agree; otherwise it is quarantined
  and recomputed, and only a gap-free verified prefix is resumed.

## Acceptance output

The demo prints one field per line, including:

```
roundtrip_match_rate=1.000
keyframe_index_hit_rate=1.000
segment_hashes[1]=<64 hex chars>
reordered_packets=…
recovered_packets=…
unrecovered_gaps=…
cache_hits=…           # every verified stage and index entry served
segment_cache_hits=…   # only whole-segment index entries served
cache_rejected=…
checkpoint_resumes=…
```

`go test ./...` exercises the same thresholds, fixed-seed hash stability, cache
rejection, crash/resume equivalence and the five-class fault matrix.

## Limits

The parser, payload codecs, cache and journal all enforce explicit bounds on
box depth, sample counts, fragment sizes, metadata payloads, key sizes and
paths. Anything outside the project's own restricted profile is rejected rather
than guessed at.
