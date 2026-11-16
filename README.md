# mediacheck

Offline, dependency-free verifier for outsourced MP4 / Matroska media
deliverables. It sniffs container type, parses structural metadata, compares
track consistency across a material group, persists every scan to SQLite and
emits a terminal summary plus an optional JSON report. Standard library only
(`struct`, `hashlib`, `argparse`, `json`, `sqlite3`); no external probing
binary or network service is ever used, and media payload bytes are never
decoded or executed.

## Install

```sh
python3.11 -m pip install -e .
```

## Generate local samples

Samples are fully synthesized (no external files):

```sh
python3.11 scripts/make_samples.py --out /tmp/media-samples
```

This writes a normal pair, a missing/extra-track pair and a truncated
Matroska whose declared elements run past end of file.

## Scan

```sh
python3.11 -m mediacheck.cli scan \
  --inputs /tmp/media-samples \
  --reference scene \
  --duration-tolerance-ms 40 \
  --db /tmp/media-check.sqlite \
  --json /tmp/media-report.json
```

Terminal summary:

```
files_total=4 files_ok=3 files_failed=1 tracks=5 max_duration_delta_ms=24 mismatches=2
```

Previous scans:

```sh
python3.11 -m mediacheck.cli history --db /tmp/media-check.sqlite
```

## How comparison works

- Files sharing a stem form one material group (e.g. `scene-a.mp4` and
  `scene-a.mkv`).
- Tracks are matched by type and normalized codec, independent of order.
- The track-richest file is the reference; others are checked against it.
- Mismatch kinds: `duplicate_track_id`, `missing_track`, `extra_track`,
  `param_diff`, `duration_delta`, `missing_duration`.
- A duration deviation exactly equal to the tolerance is consistent; a
  missing duration is reported separately, never treated as zero.
- Missing fields stay `null` rather than being filled with defaults.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | scan completed, all files parsed, no mismatch |
| 1 | scan completed but a file failed or a mismatch was found |
| 2 | invalid arguments / unusable input path (nothing scanned) |
| 3 | database or JSON report could not be written |

On a write failure the terminal summary is still printed. Report paths are
validated up front: directories, non-regular files, unwritable parents and
paths pointing back at scanned inputs are rejected.

## Limits and safety

- MP4 and Matroska only; files above 2 GiB are rejected.
- Box / EBML nesting is capped at 16 levels; element ranges are bounded.
- Reads are small and streaming (hashes use 1 MiB chunks), keeping parse
  memory well under 64 MiB.
- Out-of-range elements, integer overflow, truncated VINTs, unknown
  signatures and corrupt headers become per-file errors and never abort the
  rest of the batch.

## Tests

```sh
python3.11 -m unittest discover -s tests
```
