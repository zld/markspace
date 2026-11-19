# mkvmp4

A from-scratch, fully local **Matroska (.mkv) → MP4 stream remuxer**. It
parses EBML, maps tracks, rewrites timestamps and rebuilds a complete,
verifiable ISO BMFF index **without re-encoding any audio or video** and
without calling FFmpeg, MKVToolNix or any other external muxer.

## Scope and constraints

* Single Matroska `Segment`; supported codecs are **H.264/AVC**
  (`V_MPEG4/ISO/AVC`) video and **AAC** (`A_AAC`) audio only.
* Encrypted / content-encoded tracks and header compression are rejected
  with the offending track number; subtitle and other unimplemented track
  *kinds* are skipped with an explicit reason (never dropped silently).
* Local files only. Element size, total input size, nesting depth and sample
  count are bounded (`src/mkvmp4/__init__.py`) so a corrupt input cannot
  trigger unbounded allocation; every offset is validated.
* No GUI, no network, no camera/microphone access — terminal only.

## Architecture

```
Matroska input
   │  EBML element stream (ebml.py: IDs, VINTs, known/unknown lengths,
   │                       parent boundaries)
   ▼
matroska.py  Segment / Info / Tracks / Clusters / Cues / block parsing
   │
   ├─► timeline.py  cluster + block timecodes → per-track MP4 time base
   │                (global shift to 0, non-regression, gap/overflow checks)
   ├─► mapping.py   supported-track selection, stable MKV# → MP4 track ID
   ▼
boxes.py + mux.py  ftyp, streamed mdat (payloads copied byte-for-byte),
                   moov with rebuilt stsd/stts/stsc/stsz/stco|co64/stss
                   and edit lists; independent output verification
   ▼
report.py  element tree, mapping, per-track timestamps, MP4 box summary
   ▲
cli.py  make-fixture | inspect | remux | inspect-mp4
```

Per-track sample tables are zero-based; the original first-sample offset
removed during normalisation is preserved in an `elst` edit list so
inter-track alignment is not lost. The last sample's duration, which has no
explicit end timestamp in Matroska, is derived from `DefaultDuration` or the
median interval and always flagged as an estimate — never fabricated
silently.

## Layout

| File | Responsibility |
| --- | --- |
| `src/mkvmp4/ebml.py` | EBML IDs, variable-length integers, element boundaries |
| `src/mkvmp4/matroska.py` | Segment/Track/Cluster/Cues and block model |
| `src/mkvmp4/timeline.py` | Unified time bases, NumPy boundary/statistic checks |
| `src/mkvmp4/mapping.py` | Track selection and stable MP4 track ID mapping |
| `src/mkvmp4/boxes.py` | ISO BMFF box encoding with length backfill |
| `src/mkvmp4/mux.py` | Sample tables, mdat, moov, output verification |
| `src/mkvmp4/report.py` | Stable JSON summaries and independent MP4 parser |
| `src/mkvmp4/cli.py` | Subcommands, argument validation, exit codes |
| `tests/fixture_builder.py` | Synthetic MKV generator (unknown-length Segment, multi-track, multi-cluster) |
| `tests/test_pipeline.py` | End-to-end and negative-case tests |

## Setup and acceptance

All third-party dependencies live in a project-local `.venv`; nothing is
installed into the system Python.

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install 'numpy==1.26.4'
.venv/bin/python -m pip install -e .
.venv/bin/python -m mkvmp4.cli make-fixture --output .tmp/sample.mkv
.venv/bin/python -m mkvmp4.cli inspect .tmp/sample.mkv --json
.venv/bin/python -m mkvmp4.cli remux .tmp/sample.mkv --output .tmp/sample.mp4 --json
.venv/bin/python -m mkvmp4.cli inspect-mp4 .tmp/sample.mp4 --json
```

Run the test suite:

```sh
.venv/bin/python -m unittest discover -s tests -v
```

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Parse / mapping / mux / verification failure (corrupt or unsupported input) |
| `2` | Invalid command-line arguments |

The remux path writes to a temporary file, verifies box sizes, sample counts,
offsets and index consistency, then atomically `os.replace`s the target. On
failure the pre-existing target is left untouched and the temp file removed.
