# dashcheck

Local, dependency-free MPEG-DASH manifest builder for a single-Period,
multi-bitrate video publishing pipeline. It reads encoding-rend segment
metadata, aligns rendition boundaries by **time** (never by segment
index), and either emits a publishable static MPD with a
`SegmentTemplate`/`SegmentTimeline` or refuses to publish when the
renditions cannot be safely aligned.

All time math uses exact integer rationals (`internal/model/rat.go`);
values are never converted to floating point before comparison, and
int64 overflow is detected.

## Build

```sh
go build -o /tmp/dashcheck ./cmd/dashcheck
go test ./...
```

## Commands

```sh
# Generate a deterministic two-rendition, four-segment sample and run it.
dashcheck demo --out /tmp/dash-demo

# Publish: write an MPD only when alignment is safe.
dashcheck validate \
  --input /tmp/dash-demo/representations.json \
  --tolerance 1/1000 \
  --mpd /tmp/dash-demo/output.mpd \
  [--report /tmp/dash-demo/report.txt]

# Diagnostic only: print skews and rejection reasons, never write an MPD.
dashcheck validate --input ... --tolerance 1/20 --loose
```

`--tolerance` is an exact rational in seconds (`1/1000` = 1 ms). The
boundary check is inclusive: a skew exactly equal to the tolerance
passes. The MPD path must never equal the input path; the tool refuses
to overwrite its input.

## Input format

```json
{
  "content_id": "demo-cut-0001",
  "representations": [
    {
      "id": "video-hd",
      "bandwidth_bps": 3000000,
      "width": 1920, "height": 1080,
      "timescale": 1000,
      "segments": [
        {"number": 1, "start_tick": 0,    "duration_tick": 2000, "random_access": true},
        {"number": 2, "start_tick": 2000, "duration_tick": 2000, "random_access": true}
      ]
    }
  ]
}
```

Segment numbers may be non-contiguous but must not repeat; start ticks
must be strictly increasing; durations, bandwidth, dimensions and
timescale must be positive. Non-contiguous numbering switches the media
template from `$Number$` to `$Time$`.

## Alignment rules

1. Every rendition's ticks are normalized onto unified seconds using its
   own timescale.
2. Boundaries are clustered by normalized time. A point becomes a common
   switch point only when **every** rendition has a boundary within
   tolerance **and** every such boundary is a random-access point.
3. The start of each rendition's final (tail) segment is exempt: tail
   segments may legitimately differ between rends. Their true ranges are
   still fully expressed in each representation's `SegmentTimeline`, and
   the report shows each rendition's duration delta to the longest rend.
4. On any unalignable intermediate boundary, publish mode returns an
   alignment error and writes no MPD. Loose mode reports the same
   evidence (skew in ms, rendition id, original tick) but still writes no
   MPD. Original ticks are always retained; later segments are never
   shifted by index.

The same `model.Manifest` feeds both the MPD writer and the text report,
so packaging output and diagnostics cannot diverge. Before writing, the
MPD encoder independently re-sums every `SegmentTimeline` and rejects
the output if the sum disagrees with the model's coverage.

## Exit codes and error categories

| Exit | Meaning |
|------|---------|
| 0 | success, MPD written (publish mode) |
| 2 | input or alignment problem (`INPUT_ERROR`, `ALIGNMENT_ERROR`) |
| 3 | file write / path problem (`IO_ERROR`) |

Stable codes include `MISSING_FIELD`, `INVALID_VALUE`, `DUPLICATE_VALUE`,
`NOT_MONOTONIC`, `OVERFLOW`, `NO_COMMON_SWITCH_POINT`,
`TOLERANCE_EXCEEDED`, `BOUNDARY_NOT_RANDOM_ACCESS`, `PATH_CONFLICT` and
`WRITE_FAILED`.

## Package layout

```
cmd/dashcheck      flag parsing, orchestration, exit-code mapping
internal/model     rational math, error categories, manifest model
internal/input     strict JSON parsing and input validation
internal/align     timescale normalization and boundary matching
internal/manifest  alignment result -> reusable manifest model
internal/mpd       static MPD XML encoding with coverage verification
internal/report    per-segment template expansion and metrics
internal/fixture   deterministic demo / failure-case generators
```

## Scope

Single Period, video only, locally generated segment metadata. The tool
does not encode media, validate bitstream decodability, support DRM, live
updates or cross-Period concatenation.
