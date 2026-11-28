# media-check

Reusable Go media-processing kernel for multi-platform game releases.
It validates H.264 / H.265 / AV1 bare streams and their keyframe
indexes, checks MP4 container / track / segment constraints on one
shared integer tick line, verifies RTP packet boundaries of exported
segments, measures offscreen-decode quality, migrates versioned
manifests in stages, and publishes releases atomically with rollback.

## Build, test, acceptance

```sh
go mod tidy && go build ./... && go test ./...
go run ./cmd/media-check demo --workdir /tmp/media-check \
  --codecs h264,h265,av1 --corrupt-offset 37 \
  --interrupt-after migrate-1 --recover
```

Exit codes: 0 success, 2 input error, 3 processing interruption.
Sample assets are generated in code; no external files are needed.

## Module map

| module | role |
| --- | --- |
| `internal/clock` | single integer tick timebase, floor conversion, max alignment error |
| `internal/fault` | stable error codes with first offending input offset |
| `internal/codec` | H.264/H.265/AV1 unit parsing (truncation, bad headers) |
| `internal/index` | keyframe index validation |
| `internal/container` | MP4 build + read-back checks (go-mp4) |
| `internal/track` | track mapping rules, tick monotonicity |
| `internal/segment` | keyframe-aligned cuts, RTP boundary checks (pion/rtp) |
| `internal/manifest` | versioned manifests, strict per-version parsing |
| `internal/migrate` | staged migration with per-stage validation |
| `internal/rollback` | atomic publish pointer, recovery to complete versions |
| `internal/sample` | deterministic generated assets (streams, frames, tone) |
| `internal/pipeline` | orchestration: quality gates > budget > size |
| `cmd/media-check` | terminal entry point |

Priorities: quality gates first, then the processing budget, then
size optimization. Over budget the pipeline stops without lowering
quality gates and without publishing partial results.
