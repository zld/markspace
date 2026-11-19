# meshift

A pure-local, dependency-light **Matroska (.mkv) → MP4 remuxer**. It preserves
compressed sample bytes (no decoding or re-encoding), rewrites timestamps under
explicit deterministic rules, and emits a verifiable MP4 sample index.

Everything runs offline on macOS ARM64/x86_64. No FFmpeg, no system Python
packages, no network access.

## What it handles

- EBML variable-length integers with a hand-written parser, including
  **unknown-length elements** closed by parent/next-sibling boundaries.
- Rejection of illegal VINT widths, truncated values and out-of-bounds
  elements, each reported with its element path and byte offset.
- Non-zero origins, per-track timebases, signed relative block timecodes and
  **negative composition (PTS-DTS) offsets**, retained in a version-1 `ctts`.
- Missing, duplicated or backward timestamps, empty tracks and incomplete
  final blocks — never silently dropped; every adjustment is reported.
- Standard and segmented exports sharing one parser, with explicit
  input→output track mapping.
- 64 MiB single-file ceiling; unsupported codecs are refused rather than
  faked.
- Atomic output: a temp file is written and fully index-verified before it
  replaces the target. Existing targets are not overwritten without
  `--overwrite`.

## Layout

```
src/meshift/
  ebml.py       EBML VINT + element tree, unknown-length boundaries
  matroska.py   header, tracks, clusters and blocks (compressed bytes kept)
  timeline.py   zero-origin integer-ns normalization and rounding
  mp4.py        ftyp/moov/mdat writer and sample tables
  index.py      read-back rebuild and verification of the MP4 index
  report.py     element-tree summary, track map, first/last PTS/DTS
  cli.py        command-line entry point, exit codes, atomic writes
  errors.py     exception hierarchy and exit-code contract
scripts/make_fixture.py   synthetic MKV generator (no external media)
tests/test_roundtrip.py   round-trip and edge-case tests
```

## Setup

```bash
python3.11 -m venv .venv
.venv/bin/python -m pip install numpy==2.2.3 pytest==8.3.5
```

The source tree is laid out under `src/`, so either set `PYTHONPATH=src` or
install the package in editable mode:

```bash
.venv/bin/python -m pip install -e .
```

## Demo / acceptance

```bash
.venv/bin/python scripts/make_fixture.py --out /tmp/sample.mkv
PYTHONPATH=src .venv/bin/python -m meshift.cli convert \
    /tmp/sample.mkv /tmp/sample.mp4 --report /tmp/report.json
.venv/bin/python -m pytest -q
```

The generated fixture covers known-length clusters, an unknown-length cluster
bounded by a following sibling, a non-zero time origin, a negative PTS offset
and (via `--truncated PATH`) a truncated input.

## Command line

```bash
meshift convert INPUT OUTPUT [--report FILE] [--overwrite]
                            [--track-map IN=OUT]...
                            [--segment START_NS END_NS]
```

- `--track-map 2=1` assigns output track number 1 to input track 2; repeatable.
- `--segment 100000000 500000000` exports only samples whose DTS falls inside
  the half-open nanosecond window, reusing the same parse layer.

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | success |
| 2 | corrupt/truncated input, or MP4 index verification failure |
| 3 | bad arguments, refused overwrite, or output size limit |

## Timing rules

1. Each track's first DTS is normalized to zero.
2. DTS values are accumulated as integer nanoseconds and converted with
   half-away-from-zero rounding; equal/backward results are forced strictly
   increasing and flagged in the report.
3. The PTS-DTS composition offset is preserved exactly, including negative
   values, in a signed (version 1) `ctts` box.
4. Sample durations come from `DefaultDuration` when present, otherwise from
   adjacent DTS gaps.
5. All first/last timestamps in the report are integer nanoseconds.
