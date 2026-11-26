# replaypipe

Deterministic, reviewable clip extraction for replay events that live in
subtitles, chapter documents and timed metadata (ID3, `emsg`, SEI, RTP
side-band). Every module shares one rational media time base and converts to
integer nanoseconds; malformed or out-of-range input is reported with a stable
code, the owning track and a byte offset instead of being repaired.

## Requirements

- macOS (Darwin), Go 1.23.12
- No network access at run time and no external media
- Pinned dependencies: `github.com/abema/go-mp4 v1.7.4`, `github.com/pion/rtp v1.8.27`

```
brew install goenv && eval "$(goenv init -)"
goenv install 1.23.12 && goenv local 1.23.12
go mod download
go mod tidy && go build ./... && go test ./...
```

## Acceptance run

```
go run ./cmd/replaypipe verify --scenario all --root "$(mktemp -d)" \
  --pre 5s --post 8s --min-width 1280 --min-fps 30 \
  --max-work-bytes 67108864 --max-output-bytes 16777216
```

Exit codes: `0` every checked path matched, `1` a check did not match, `2`
argument or runtime error.

The command generates its own fixtures, so `--root` may be empty. It prints:

- round-trip parse consistency (`18/18`, 100%) across SRT, WebVTT, chapters,
  ID3, SEI and RTP
- keyframe hits `8/8`, three slices, and a per-slice SHA-256
- rerun: identical hashes, `reused=3`, `recomputed=0`
- envelope-only change: content digest unchanged, envelope digest changed,
  three slices reused
- single-event change: exactly one slice re-cut, two reused
- corrupt inputs: `BOX_TRUNCATED`, `TIME_REGRESSION`, `TIME_OVERFLOW`,
  `EVENT_OUT_OF_RANGE` with byte offsets, and an unchanged manifest hash
- migration: parse → normalise → validate digest → atomic switch, rollback
  retained, interrupted run resumed idempotently

## Layout

| Path | Role |
| --- | --- |
| `internal/timebase` | rational clock, exact nanosecond mapping, coded errors |
| `internal/metadata` | SRT/WebVTT, chapter JSON, ID3, emsg, SEI, RTP parsers |
| `internal/index` | MP4/CMAF box walk, content/envelope/event digests, keyframes |
| `internal/mp4box` | deterministic ISO BMFF box + movie builder |
| `internal/clip` | keyframe-aligned planner and sample-copy writer |
| `internal/manifest` | versioned adapter and atomic migration |
| `internal/store` | journaled atomic commit with rollback point |
| `internal/fixtures` | self-generated healthy, variant and defect samples |
| `cmd/replaypipe` | terminal acceptance entry point |

## Design notes

- **Digests, not paths.** Media identity is the ordered sample timeline plus a
  SHA-256 per payload. The box envelope is hashed separately, so re-muxing
  updates the index/manifest without re-cutting samples. Event identity hashes
  canonical event payloads only.
- **Cut points.** Each event gets a window of `pre` before to `post` after.
  The start cut is the best decodable keyframe inside that window; the end cut
  covers the event. The manifest keeps the original event instant.
- **Budget order.** Minimum width and frame rate first, then the processing
  byte budget, then the smallest qualifying representation. Samples are never
  re-encoded; anything that cannot be satisfied fails closed.
- **Atomic delivery.** Assets are written to temp files and re-parsed before
  renaming; the manifest switches last and is journaled. A failure leaves the
  previous manifest (and its rollback copy) untouched.
