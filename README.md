# hlsforge

`hlsforge` turns locally delivered, possibly out-of-order CMAF
(`moof`/`mdat`) fragments into a low-latency HLS presentation and verifies
that every bitrate tells the same timeline story. It is a single binary with
no runtime third-party dependencies (Go 1.22 standard library only).

## Why

Replay ingest nodes report fragments concurrently. Network jitter produces
reordering, duplicates, late arrivals and loss. A naive packager shifts later
fragments forward to hide gaps, which makes the replay window silently skip
content. `hlsforge` instead keeps every time slot explicit: missing fragments
become `GAP` markers, late fragments are rejected once their window is
finalized, and cross-bitrate drift is measured rather than papered over.

## Pipeline

```
fixture / local fragments
        -> ingest.Collector   (dedupe, conflict, gap, out-of-window reject)
        -> cmaf parser        (box bounds, truncated tables, overflow)
        -> timeline.Build     (integer-ms normalization, sync ordering)
        -> segment.Layout     (parts, segments, rolling window, sync cuts)
        -> segment.Persist    (atomic temp-file + rename)
        -> manifest generator (master + media + PART + PRELOAD-HINT)
        -> verify             (files, SHA-256, drift, canonical re-render)
```

All time arithmetic uses integer milliseconds (`value*1000/timescale`); no
floating point is used on the time path, so long runs cannot accumulate
rounding error.

## Commands

### demo

Generate deterministic CMAF samples with optional fault injection.

```
hlsforge demo --out ./inbox --seed 7 \
  --bitrates 800000,1600000 --duration 12s --part 500ms \
  --fault missing,order,drift
```

Faults:

- `missing` — drop one mid-window fragment on the last bitrate (a retained gap);
- `order`   — shuffle delivery order and duplicate one entry (idempotent path);
- `drift`   — offset the first bitrate's `tfdt` by a fixed 80ms.

### build

Ingest an inbox and write the HLS tree.

```
hlsforge build --in ./inbox --out ./hls \
  --from-seq 1 --to-seq 24 \
  --segment 2s --part 500ms --window 12s
```

Gaps stay in place as `*.gap` markers; a rolling `--window` evicts old
segments but keeps absolute media sequence numbers.

### verify

Re-read a build and re-render the canonical manifest with the same generator,
then diff it field by field.

```
hlsforge verify --in ./inbox --hls ./hls \
  --from-seq 1 --to-seq 24 --segment 2s --part 500ms --window 12s \
  --duration 12s --tolerance 50ms [--json]
```

## Exit codes

| Code | Meaning                                              |
|------|------------------------------------------------------|
| 0    | success, or a build/demo that retained expected gaps |
| 1    | parameter or file error                              |
| 2    | verifier found a content consistency failure         |

## Failure codes

`MISSING_PART`, `TIMELINE_DRIFT`, `FRAGMENT_CONFLICT`, `LATE_OUT_OF_WINDOW`,
`HASH_MISMATCH`, `MISSING_FILE`, `MANIFEST_MISMATCH`, plus parse-level
`BOX_OVERFLOW`, `TRUNCATED_SAMPLE_TABLE`, `ZERO_DURATION_SAMPLE` and
`INTEGER_OVERFLOW`.

## Safety limits

- max single box size: 16 MiB (`cmaf.MaxBoxSize`);
- max fragment size: 16 MiB (`cmaf.MaxFragmentSize`);
- max segments per build: 4096 (`segment.MaxSegments`);
- max rolling window: 60s (`segment.MaxWindowMs`).

These bounds reject malformed or hostile inputs before large allocations.

## Demo and tests

```
./scripts/demo.sh
go test ./...
```
