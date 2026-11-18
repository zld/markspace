# mediascan

Local, dependency-free index and consistency checker for audio/video
containers. Built for media-pipeline packaging: one extractor feeds `scan`,
`check`, and `pack`, and every run can leave a JSON report behind.

## Requirements

- macOS (Apple Silicon or Intel) or Linux
- Python 3.11
- `pytest==8.3.5` installed only in the project `.venv`

## Install

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install -e '.[test]'
```

## Quick start

```sh
.venv/bin/python tools/make_samples.py --out .tmp/samples
.venv/bin/python -m mediascan.cli check .tmp/samples \
  --baseline .tmp/samples/reference.json \
  --db .tmp/index.sqlite \
  --json .tmp/report.json \
  --duration-tolerance-ms 25
```

Latest line printed:

```
files_scanned=4 tracks_total=7 mismatches=3 failures=1 max_duration_delta_ms=120
```

The bundled samples intentionally contain one corrupt MP4 (`scene_broken.mp4`,
reported with `ERR_BOX_LENGTH` and its byte offset) and one drifted MP4
(`scene_b.mp4`: timescale, channel count, and +120 ms audio duration).

## Commands

| command | purpose |
| --- | --- |
| `scan <paths>` | extract metadata and optionally index it in SQLite |
| `check <paths> --baseline FILE` | compare against a baseline and report mismatches |
| `pack <paths>` | re-verify files with the same extractor before packaging |

Common options: `--db PATH`, `--json PATH`, `--no-recursive`,
`--max-file-size BYTES`, `--max-files N`.

`check` options: `--duration-tolerance-ms MS` (default 25),
`--relative-tolerance-pct PCT` (optional; a drift must exceed both gates),
`--base-dir DIR` (baseline paths are relative to the scan root by default).

`pack` options: `--require-known` treats unknown metadata as a failure.

Exit codes: `0` clean, `1` mismatches or per-file failures, `2` invalid
arguments/baseline/limits.

## Supported containers

- WAV (RIFF) and RF64
- AIFF / AIFC
- ISO BMFF: MP4, M4A, MOV (brand-driven)

The container is identified from signatures and box structure. Extensions are
recorded as hints only (`extension_matches`), never used to pick a parser.
Unknown, truncated, or malformed inputs fail with a stable `error_code` and a
byte `error_offset`.

## Metadata and rules

Per track: type (audio/video/subtitle/other), codec, sample rate, channels,
bit depth, dimensions, timescale, track duration. Missing values stay `null`
and are listed under `unknown_fields`; nothing defaults.

Tracks are matched by type and ordinal position within that type. Rules:

| error code | meaning |
| --- | --- |
| `ERR_DURATION_DRIFT` | duration deviation exceeds tolerance |
| `ERR_TIMEBASE_DRIFT` | timescale differs from baseline |
| `ERR_CODEC_PARAM` | one encoding parameter differs |
| `ERR_PARAM_UNKNOWN` | a value is missing on one side |
| `ERR_TRACK_MISSING` / `ERR_TRACK_EXTRA` | track counts differ |
| `ERR_TRACK_TYPE` | a track changed type |
| `ERR_BASELINE_MISSING` | file has no baseline entry |

## Index

`--db` writes a SQLite index. Each file is committed in its own transaction;
failures are recorded without touching other rows, and re-scanning the same
path updates the existing row and replaces its tracks instead of duplicating.

## Reports

`--json` writes `{summary, files, mismatches, failures}` with stable sorting,
using a temp file plus atomic replace. If writing fails, the terminal summary
is still printed and a warning goes to stderr.

## Limits and safety

`mediascan/limits.py` bounds file size, box count, nesting depth, track count,
sample entries, and read chunk sizes. Only local files are read; nothing
executes embedded media.

## Tests

```sh
.venv/bin/python -m pytest -q
```

Fixtures are generated on the fly in pytest temporary directories by
`tools/make_samples.py`; no media binaries are committed.
