# mediamux

Reusable Go media-processing primitives for regional live distribution. It
parses HLS/DASH manifests, inspects fragmented MP4 and RTP streams, and caches
segments by content address so checkpoints can resume without re-reading or
re-analysing accepted work. Everything runs locally, reads no network input,
and depends only on `github.com/abema/go-mp4` and `github.com/pion/rtp`.

## Layout

| Path | Responsibility |
| --- | --- |
| `media/timebase.go` | Exact integer times, cross-timescale conversion, rational remainders |
| `media/errors.go` | Structured errors carrying phase, node, segment, track and byte offset |
| `manifest/hls.go` | HLS target duration, `EXTINF`, discontinuity and map parsing |
| `manifest/dash.go` | DASH representation, timescale, `SegmentTemplate`/`SegmentTimeline` |
| `manifest/iso8601.go` | Exact ISO-8601 duration parsing |
| `stream/reader.go` | Bounded chunk reader; cancellation closes blocked reads |
| `stream/backpressure.go` | Fixed-capacity queue; producers block, slow consumers stop the branch |
| `analyze/mp4.go` | go-mp4 box/track/sample analysis with damage recovery |
| `analyze/box.go` | Box header validation, boundary recovery, digest helper |
| `analyze/rtp.go` | pion/rtp sequence, timestamp and payload validation |
| `analyze/quality.go` | Sliding-window bitrate, keyframe gaps and quality summary |
| `cache/cas.go` | In-memory content-addressed store with provenance checks |
| `cache/disk.go` | On-disk CAS that persists bodies, records and provenance |
| `pipeline/checkpoint.go` | Atomic checkpoint with cursor and stage state |
| `pipeline/runner.go` | Parse → analyse → cache → deliver, with resume |
| `pipeline/alignment.go` | Manifest-versus-media duration comparison |
| `cmd/mediad/main.go` | Terminal acceptance adapter |

## Design notes

* **One integer timeline.** Every duration becomes an exact `media.Ratio`.
  Truncation to the unified 90 kHz timeline keeps the leftover rational and
  `AlignAccumulator` sums it, so reported drift is exact and never a float
  accumulation.
* **Damage is data, not a crash.** An impossible box length is recorded with
  its byte offset and scanning resumes at the next aligned box boundary. The
  segment is marked corrupt, the pipeline continues, and the exit status says
  whether diagnosis completed.
* **Transport versus container.** RTP sequence gaps are reported separately
  from MP4 box damage. Sequence numbers wrap modulo 65536; duplicate packets
  are counted once and excluded from the byte total.
* **Cache hits must be proven.** A stored segment is only skipped when its
  recorded origin node, input length, body SHA-256 and analysis-record digest
  all match. Anything else is quarantined, reported as a source rejection, and
  rebuilt from input.
* **Checkpoints follow the consumer.** A segment is committed only after the
  bounded queue accepted it, so a slow consumer stops the branch at the last
  fully delivered segment and a resume replays from exactly there.
* **Bounded memory.** Streamed sources are read through `ChunkReader` under the
  context. The working set is one segment plus the queue, checked against
  `--memory-limit` before any read starts.

## Running

```sh
brew install go@1.23
# use the versioned toolchain if `go` on PATH is newer
export PATH="/opt/homebrew/opt/go@1.23/bin:$PATH"

go mod tidy
go build ./...
go test ./...

go run ./cmd/mediad demo --seed 7 --memory-limit 1048576 --slow-consumer-ms 20
```

The demo generates HLS and DASH manifests, MP4 fragments and RTP packet streams
in memory from the seed, then runs:

1. a clean path (reference digest),
2. truncated and bad-box-length media,
3. duplicate and lossy RTP streams,
4. a manifest whose declared durations contradict the media,
5. an interrupted run resumed from its checkpoint, compared with a cold run,
6. a replay where cached entries claim a forged source node,
7. a stalled consumer that trips the back-pressure budget.

Exit codes: `0` all invariants held; `2` bad input or arguments; `3` damage was
detected and diagnosed (only with `--strict`); `4` a recovery or internal
invariant failed.

The demo intentionally prints damage findings while exiting `0`, so it can be
used as a demonstration; `--strict` maps a diagnosed damage result to exit `3`.
