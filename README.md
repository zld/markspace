# mediacheck

Offline metadata extraction and cross-version consistency checks for media
packaging submissions. Pure Python 3.11 standard library; macOS friendly.

## Install and run

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install -e '.[test]'
.venv/bin/python scripts/make_samples.py --out .sample-media
.venv/bin/python -m mediacheck scan .sample-media --db .results.sqlite --json .report.json
.venv/bin/python -m mediacheck compare .sample-media --db .results.sqlite --duration-tolerance-ms 40
.venv/bin/python -m mediacheck pack .sample-media/clip-a.mkv --out .pack.json
.venv/bin/python -m unittest discover -s tests
```

Exit codes: `0` all checks consistent, `1` inconsistencies reported, `2` usage,
environment, or database error. With the generated samples `scan` reports
`files=3`, `tracks=5`, `duration_deviation_ms=80`, and exit code 1 because the
MP4 version has one track instead of two and the `.mkv` versions differ in
duration by 80 ms.

## Supported containers (intentional subset)

* EBML/Matroska-like: `EBML` header with `DocType`, `Segment` with
  `Info/TimecodeScale`, `Info/Duration`, and `Tracks/TrackEntry` elements
  (number, type, codec id, codec delay, dimensions, sample rate, channels and
  a project `TrackDuration` element `0x4D7D`).
* ISO BMFF: `ftyp` plus `moov/mvhd`, and per `trak`: `tkhd`, optional
  `edts/elst`, `mdia/mdhd`, `mdia/hdlr`, `mdia/minf/stbl/stsd` sample entry.

Extensions are ignored; dispatch is by magic bytes. Unknown fields are reported
as `null`/`unknown`, never filled with defaults. Malformed input yields an error
with the byte offset where the structure stopped making sense.

## Rules

| Rule | Code | Meaning |
| ---- | ---- | ------- |
| R001 | `container-error` | unreadable/truncated structure, with offset |
| R002 | `track-count-drift` | track count differs from the reference file |
| R003 | `track-kind-drift` | track kind list differs from the reference |
| R004 | `timescale-drift` | container or track time base differs |
| R005 | `codec-param-drift` | codec id or key parameters differ |
| R006 | `duration-deviation` | duration differs beyond the tolerance |
| R007 | `duplicate-track-number` | duplicate track numbers inside one file |
| R008 | `track-duration-overflow` | track start+duration exceeds container |
| R009 | `unknown-metadata` | field could not be extracted (error in `--strict`) |
| R010 | `input-added`/`input-changed` | `compare` baseline differences |

Files in the same directory form a version group; the lexicographically first
relative path is the reference. Directories are compared independently.

## Options

`scan` and `compare` accept `--db`, `--json`, `--exclude PATTERN` (repeatable),
`--strict`, `--duration-tolerance-ms MS`, and `--max-depth N` (0–16). Scans are
recursive, never follow symlinks, reject paths that escape the root, and read at
most 256 MiB per file. Paths are sorted and JSON output uses fixed, sorted
fields; files are written to a temporary file and atomically replaced.

`compare` saves the current batch and reports content changes relative to the
previously stored batch (`baseline_batch_id` in the JSON payload).

## Database

SQLite stores per batch: batch id (SHA-256 over rule version, options, and input
hashes), creation time, rule version, options, input SHA-256/size, full parse
results, and issues. Re-running the identical command updates the same batch
row (idempotent); each batch is written in a single transaction, so a failure
rolls back and leaves no partial rows.

## Layout

```
src/mediacheck/  probe.py models.py rules.py db.py pack.py cli.py
scripts/         make_samples.py
tests/           test_probe.py test_batch.py
```
