# dashgen

Offline DASH MPD manifest generator and cross-rendition time-alignment
checker for game-event replay pipelines. It reads a local JSON segment index,
performs all time math with exact integer rationals, writes a DASH
`SegmentTemplate`/`SegmentTimeline` manifest, and can verify a written
manifest back against its source plan. No transcoding and no network access;
the Go standard library is the only dependency.

## Requirements

- Go 1.22 or newer (`brew install go`)
- Builds and runs on macOS arm64 and x86_64.

## Layout

| Path | Responsibility |
| --- | --- |
| `cmd/dashgen` | Subcommand parsing, exit codes, terminal output |
| `internal/plan` | JSON segment index parsing and structural validation |
| `internal/timebase` | Exact rational arithmetic, tick↔ms conversion, durations |
| `internal/align` | Timeline integrity and cross-timescale boundary comparison |
| `internal/manifest` | MPD object model and SegmentTimeline compression/expansion |
| `internal/packager` | Assembles the representation layer from an aligned plan |
| `internal/mpd` | Template expansion, XML marshalling, parser, atomic writer |
| `internal/demo` | Built-in three-rendition, six-segment sample plan |
| `internal/validate` | Tolerance checking and MPD↔plan reconciliation |

## Quick start

```sh
go test ./...
DASH_TMP="$(mktemp -d)"
go build -o "$DASH_TMP/dashgen" ./cmd/dashgen

"$DASH_TMP/dashgen" demo --out "$DASH_TMP/input"

"$DASH_TMP/dashgen" build \
  --input "$DASH_TMP/input/plan.json" \
  --out "$DASH_TMP/stream.mpd" \
  --template 'seg-$RepresentationID$-$Number%05d$.m4s' \
  --start-tolerance 20ms --duration-tolerance 2ms

"$DASH_TMP/dashgen" verify \
  --mpd "$DASH_TMP/stream.mpd" \
  --plan "$DASH_TMP/input/plan.json"
```

A successful run prints:

```
representation_count=3
expanded_segment_count=18
max_start_offset_ms=12.500
max_duration_delta_ms=1.000
template_example=seg-r360-00001.m4s
```

## Subcommands

### demo --out DIR

Generates `DIR/plan.json`: three renditions (`r360` @300 tps, `r720`
@90000 tps, `r1080` @24000 tps) of six segments each, with different origins
and tick rounding. The sample deterministically produces a 12.5ms maximum
start offset and a 1.0ms maximum duration delta.

### build --input plan.json --out stream.mpd --template TEMPLATE [flags]

Parses and validates the plan, aligns the renditions, expands every segment
path, compresses each timeline and atomically writes the MPD. The manifest is
written even when tolerance or timeline validation fails so it can be
inspected; the process still exits non-zero in that case.

Optional flags:

- `--init-template TEMPLATE` — initialization segment pattern.
- `--start-tolerance DURATION`, `--duration-tolerance DURATION` — limits in
  `us`/`ms`/`s` (e.g. `20ms`, `1.5s`). Omit to report measured maxima without
  rejecting anything.

### verify --mpd stream.mpd --plan plan.json [flags]

Parses both files, expands the manifest timelines back to native ticks,
reconciles every representation and segment against the plan, then applies
the same optional tolerances. Reconciliation mismatches and tolerance
violations are printed individually.

## Template placeholders

`$RepresentationID$`, `$Number$`, `$Number%0Nd$` (zero-padded width),
`$Time$` and the escaped `$$`. Unknown or unterminated placeholders are
errors. Expanded paths must be relative and contain no `.`/`..` component,
and no two segments may expand to the same output path.

## Exact time math

Each timestamp is a reduced `num/den` rational backed by `math/big`;
timescales are compared cross-rendition without floating point, so no
rounding error accumulates over long timelines. Durations and offsets render
with fixed three-decimal millisecond formatting using integer rounding.

SegmentTimeline entries are compressed into `r` repeats for contiguous
equal-duration segments; the first segment, any gap/jump and any duration
change force an explicit `t`.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 2 | Parameter or input error (bad flags, malformed JSON/template, traversal) |
| 3 | Timeline validation failure (overlap, out-of-order, missing, tolerance) |
| 4 | File read/write failure |

## Limits

At most 32 representations and 100000 segments per representation. File
writes go through a synced temporary file renamed into place, so a failed
write never leaves a half-written manifest.
