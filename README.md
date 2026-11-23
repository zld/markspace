# archivecheck

An auditable recovery kernel for historical HLS/DASH archives. It parses a
local playlist and fragmented MP4 media, reconstructs an RTP stream that may
have been duplicated, reordered or lossy, locates damage on the media timeline,
runs the analysis as a checkpointed dependency graph, and emits evidence that a
later reviewer can re-verify.

Everything is local and deterministic. No network access, no DRM, terminal only.

## Quick start

```sh
go mod tidy
go build ./...
go test ./...

# Clean recovery.
go run ./cmd/archivecheck demo --seed 42 --fault none

# A burst loss that straddles a segment boundary.
go run ./cmd/archivecheck demo --seed 42 --fault burst-loss \
    --checkpoint /tmp/archivecheck-state.json

# Resume the same run; every confirmed node is replayed, not re-executed.
go run ./cmd/archivecheck demo --seed 42 --resume \
    --checkpoint /tmp/archivecheck-state.json
```

`demo` generates a fresh fixture in a temporary directory (or the one given by
`--root`) and analyses it. `gen` only writes the fixture, and `check` analyses an
existing one.

### Exit codes

| code | meaning |
| ---- | ------- |
| 0 | completed and verified |
| 2 | input or manifest format error (source position reported) |
| 3 | damage located in media |
| 4 | dependency blocked or checkpoint inconsistent |

### Summary fields

| field | meaning |
| ----- | ------- |
| `manifest_segments` | distinct segments on the shared timeline |
| `representations` / `tracks` | DASH layers / fMP4 tracks |
| `keyframe_interval_ms_p50` | median sync-sample interval |
| `keyframe_interval_ms_distribution` | interval histogram (JSON) |
| `damage_ranges` | number of merged damaged intervals |
| `damage_range=...` | interval bounds, confidence and affected segments |
| `missing_packets` / `duplicate_packets` | transport-level counts |
| `first_bad_segment` | earliest segment overlapping damage |
| `first_blocked_dependency` | node that blocked the certification path |
| `resumed_nodes` | nodes a resume had to re-execute (0 when fully confirmed) |
| `resume_ms` | wall time of this run |
| `output_sha256` | digest of the auditable result |

`--json` emits the whole `pipeline.Report` instead.

## Layout

```
cmd/archivecheck        terminal entry point
internal/model          exact integer timeline, rational remainders, faults
internal/manifest       HLS and DASH parsers producing one segment plan
internal/transport      RTP reorder, dedup, gap events (pion/rtp wire format)
internal/inspect        fMP4 structure, sample tables, keyframes, damage mapping
internal/scheduler      dependency DAG, idempotent digest-keyed retries
internal/checkpoint     atomic, digest-verified resume store
internal/fixture        deterministic fixture and RTP capture generator
internal/pipeline       manifest -> reorder -> inspect -> merge -> certify
```

Data flow: the manifest yields a segment plan; the transport stage orders each
segment's packets and reports gaps; inspection decodes the matching fragment,
measures keyframe cadence and maps gaps to media time; the DAG runs these
per-segment tasks and a merge/certify tail; the checkpoint records every
terminal node so a rerun replays rather than repeats.

## Guarantees

- **Exact time.** Durations are converted to one shared timebase with integer
  arithmetic. The remainder of every conversion is carried, never rounded away,
  and a conversion that is not integral is an error with a source position.
  `--allow-inexact` opts into carrying the fraction instead.
- **No fabricated media.** A missing RTP packet produces a gap event and a
  damaged interval; it never becomes a synthetic sample. Sequence numbers are
  unwrapped across the 16-bit wrap, duplicates are counted once, and a repeat
  with a different payload digest is reported as a collision.
- **Located damage.** Structural faults carry the fragment name and byte range;
  media faults carry the shared-timebase interval, the confidence (`exact`,
  `estimated`, `unknown`) and the offending segment.
- **Merged but honest.** Damage ranges separated by at most one target frame
  merge, so a gap across a segment boundary is one interval; wider separations
  stay distinct.
- **Replayable scheduling.** A node's identity is its input digest plus its
  parameter digest. Retries reuse the same key, so a retried node never submits
  two artifacts. A failed dependency reports the first blocked node in
  topological order.
- **Resumable and verifiable.** Checkpoints are written to a temp file, fsynced
  and atomically renamed, then verified against a format version, a whole-file
  digest and per-entry digests. Repeating a run with the same inputs and options
  produces the same `output_sha256`.
- **Bounded memory.** A fragment is rejected above `MaxFragmentBytes`, raw bytes
  are released once sample metadata is extracted, and the reorder buffer and
  DASH timeline expansion are both capped.

## Fixture faults

`demo`/`gen --fault`:

| fault | what it injects | expected exit |
| ----- | --------------- | ------------- |
| `none` | clean archive | 0 |
| `dup-reorder` | reordered arrivals and exact duplicates | 0 |
| `burst-loss` | loss spanning two adjacent segments | 3 |
| `truncated-box` | a media segment cut inside its mdat | 3 |
| `keyframe-skew` | low layer with a different GOP cadence | 3 |
| `duration-skew` | low layer whose media is shorter than declared | 3 |
| `digest-conflict` | a segment whose bytes no longer match the catalog | 3 |
| `inexact-timebase` | an EXTINF value not representable in the timebase | 2 |
| `manifest-boundary` | a byte range with no resolvable offset | 2 |
| `rtp-collision` | one sequence number with conflicting payloads | 3 |

The default archive has 12 segments per representation, two representations
(one audio/video pair equivalent), and the burst-loss case produces one
cross-boundary damage range.
