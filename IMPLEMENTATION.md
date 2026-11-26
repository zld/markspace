# Implementation map

The verification command is the only entry point a reviewer needs. This file
maps the requirements from the brief onto the code so each one can be checked
in isolation.

## Layout

| Path | Package | Responsibility |
| --- | --- | --- |
| `replay/engine.go` | `.` | parse → resolve → repair → window → pipeline → publish |
| `replay/verify.go` | `.` | fixture generation, cancellation and resume self-tests |
| `replay/checkpoint.go` | `.` | numbered release record, digests, resume validation |
| `replay/config.go` | `.` | parameters, validation, cancellation mapping |
| `internal/media` | `media` | rational time base and the shared error model |
| `internal/parse` | `parse` | MP4 box walk, MXF KLV walk, metadata resolution |
| `internal/repair` | `repair` | timestamp repair with before/after audit |
| `internal/event` | `event` | RTP marker events and clip windows |
| `internal/index` | `index` | per-sample index, keyframe seek, stable JSON |
| `internal/flow` | `flow` | bounded worker pool, backpressure, ordered merger |
| `internal/output` | `output` | clip writer, index/summary publishing, digest |
| `internal/fixtures` | `fixtures` | generated MP4/MXF samples (no external media) |
| `cmd/replaycheck` | `main` | flag parsing, printing, exit codes only |

## Requirement trace

**1. One time and error model.** `media.Rational` keeps every instant exact in
lowest terms; `Micros()` rounds half away from zero and `MicrosCeil()` gives the
absolute upper bound used for reported deviations. `media.Error` carries code,
file, byte offset, track and time range, and `media.CodeOf` maps any wrapped
error to a stable code.

**2. Metadata resolution.** `parse.ResolveMetadata` adopts a field present in a
single source or agreeing across sources, honours `Selectors` per field, and
otherwise reports `META_CONFLICT`. A conflict on the primary pair aborts the run
before any clip is written, and `Summary.Metadata` records the candidates and
the chosen source (`internal/parse/metadata.go`).

**3. Timestamp repair.** `repair.RepairTrack`: a zero or negative duration
inherits the previous repaired duration; a DTS at or before the previous end is
moved to that end and classified `duplicate` or `backwards`; legal gaps are
left untouched. Every change is recorded with both the before and after DTS and
duration (`internal/repair/timestamp.go`).

**4. Events and windows.** `event.Stream.Events` keeps only marker packets and
ignores any repeated sequence number after its first delivery.
`event.BuildWindows` starts from `[event-pre, event+post)`, aligns video back to
the latest keyframe at or before the boundary and audio to the video-aligned
boundary, and records both deviations per window (`internal/event/`).

**5. Index and seek.** `index.Build` records track, DTS/end/PTS, byte offset,
size and keyframe flag for every sample. `TrackIndex.Seek` returns the latest
decodable entry at or before the target, flagged exact or not. `Marshal` writes
a sorted, byte-stable JSON document (`internal/index/`).

**6. Pipeline, merge, checkpoint.** `flow.Pipeline` runs cfg.Workers goroutines
over a channel of capacity cfg.Queue; a slow consumer blocks the workers, which
blocks the producer. A consumer error or context cancellation cancels the
producer (`internal/flow/pool.go`). `flow.Merger` releases only the contiguous
sequence prefix (`internal/flow/merge.go`), and `replay.Checkpoint` records the
last released sequence with an input and parameter digest that must match
before a resume (`replay/checkpoint.go`).

**7. Failure positions and reproducibility.** Truncated boxes report the byte
where the file ends and name the box that overran it; MXF KLV overruns report
the KLV offset; metadata conflicts report the header offset. Bad cases are
never published. `Summary.Canonical()` hashes the sample map, clip hashes,
metadata choices, repairs, rejected cases and the deterministic metrics;
wall-clock and scheduling counters are excluded so the digest is stable.

## Verification

```
go test ./...
go run ./cmd/replaycheck verify --seed 7 --out "$TMPDIR/replay-proof" \
    --workers 3 --queue 2 --delay 20ms
```

### Toolchain note

The brief asks for Go 1.23.12 and pion/rtp v1.10.6. That pion release declares
`go 1.24.0` and refuses to build under a 1.23 language version, so `go.mod`
carries `go 1.24.0`. With the default `GOTOOLCHAIN=auto` the Go 1.23.12 binary
installed from `go@1.23` reads that directive and runs the build with the
matching 1.24 toolchain automatically; `go version` after that reports
`go1.24.0`. Nothing else in the project needs a feature newer than 1.23.

Acceptance output: `map_count=16`, `clips=2`, `max_time_error_us<=11112`,
`byte_error=0`, `bad_cases=3`, `queue_peak<=2`, `resume_digest_equal=true`,
and a 64-character `digest_sha256`. Rerunning with the same seed prints the same
digest. Exit codes: 0 success, 2 corrupt input or unresolved conflict, 130
cancellation.

`--delay` spaces the workers to make backpressure observable; it is excluded
from the parameter digest because it cannot change output.
