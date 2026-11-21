# mediidx

Offline ISO BMFF (MP4) **sidx rebuild and sample-table cross-validation** for
game asset archiving.  It derives fragment ranges from the sample table,
rewrites the index of a copy of the file, and then re-parses the output with
an independent validator to prove that every index reference starts exactly at
a chunk byte offset and spans exactly those chunks' bytes.

Everything is pure Python 3.11 standard library: no playback framework, no
third-party media parser, no network access, no graphical interface.

## Scope

Accepted inputs are classic (non-fragmented), unencrypted MP4 files with:

* exactly one `trak` with a `vide` handler,
* exactly one top-level `mdat`,
* `stts`, `stsc`, `stsz` and exactly one of `stco` / `co64`.

Rejected with exit code 2 (structure/consistency):

* truncated or out-of-range boxes, duplicate critical tables, integer overflow,
  `stz2`/`ctts`, encrypted or fragmented boxes (`senc`, `saiz`, `tenc`,
  `moof`, `traf`, `mvex`, ...),
* sample-table count mismatches, chunks outside the `mdat`, overlapping or
  gapped chunk ranges, non-contiguous media,
* more than one track or one `mdat`, non-video handlers,
* more than one top-level `sidx`.

Rejected with exit code 1 (usage / file I/O):

* unreadable or missing input, unwritable output, `--out` equal to the input,
* unparsable `--target-duration`, `--timescale` or `--earliest-presentation-time`,
* a missing `--timescale` when the input has no readable `sidx` to inherit it
  from.

## Install (macOS Terminal)

```sh
brew install python@3.11
python3.11 -m venv .venv
.venv/bin/python -m pip install -e .
```

All commands below use `.venv/bin/python`; nothing is installed into the
system interpreter.

## Demo

```sh
.venv/bin/python -m mediidx fixture --out /tmp/sample.mp4
.venv/bin/python -m mediidx rebuild /tmp/sample.mp4 --out /tmp/rebuilt.mp4 --target-duration 2 --json
.venv/bin/python -m mediidx validate /tmp/rebuilt.mp4 --json
.venv/bin/python -m unittest discover -s tests -v
```

The default fixture has one video track at timescale 1, six samples with decode
durations `1, 1, 2, 1, 1, 2` seconds, one chunk per sample, and sample sizes
`100, 120, 80, 140, 100, 160` bytes.  The rebuild summary contains:

```json
{
  "sample_entries": 6,
  "index_offset_check": "ok",
  "boundaries": [[0, 2], [2, 4], [4, 6]],
  "reference_count": 3
}
```

`validate` reports the same sample count and boundaries, and each
`offset_checks[]` item includes `reference_offset`, `referenced_size`,
`expected_offset`, `expected_size`, `reference_duration`, `expected_duration`
and a per-item `status`.  A reference is only `ok` when its start byte equals
the first covered chunk's offset and its size equals the summed size of the
chunks it covers.

## Commands

### `fixture --out PATH`

Writes the deterministic acceptance fixture.  Generated entirely from Python
literals.

### `rebuild INPUT --out PATH --target-duration T [--json]`

* `--target-duration` is a **packing threshold in sidx timescale ticks**, not a
  hard cut.  Segments always end on whole-chunk boundaries.
* A segment starts with the first chunk and takes at least a second chunk when
  one is available, then keeps taking chunks while the accumulated decode
  duration stays at or below the target.  The selected boundary is therefore
  the closest whole-chunk boundary that does not exceed the target.
* A chunk whose own duration already exceeds the target becomes its own
  segment; the reason is recorded in `segments[].isolated_reason` and echoed
  in `warnings`.
* `--timescale`, `--earliest-presentation-time` and `--sidx-version` default to
  the values of an existing readable `sidx`.  Without one, `--timescale` is
  required and the other two default to `0` / version `0`; supply them
  explicitly when the input cannot disambiguate them.
* The source file is never modified.  If `--json` is used the JSON document is
  written to stdout; diagnostics and warnings go to stderr.
* The rebuilt bytes are re-parsed and validated in memory, written to a
  temporary file, validated again from disk, and only then renamed onto
  `--out`.  Any failure removes the temporary file (and never publishes the
  destination), so a failed run leaves no plausible-looking output.

### `validate INPUT --json`

Re-runs the box, `stbl` and `sidx` parsers from scratch and reports one record
per index reference plus coverage/duration summaries.  Exit code 2 means the
index disagrees with the sample table (or the container is malformed).

## Output

stdout is always exactly one JSON object.  Human-readable errors and warnings
go to stderr.  Exit codes:

| code | meaning |
|------|---------|
| 0    | success |
| 1    | bad arguments or a local file read/write failure |
| 2    | input structure or consistency error |

## Limits

`MAX_FILE_BYTES` 256 MiB, box nesting depth 8, 100 000 boxes, 5 000 000
samples/chunks, 2 000 000 table entries, 65 535 `sidx` references.  Table entry
counts are checked against the actual payload size before any allocation.

## Layout

```
src/mediidx/
  box_reader.py    bounded box tree, 32/64-bit and end-of-file sizes
  cursor.py        bounded big-endian payload reader
  errors.py        exit-code taxonomy carrying box type and offset
  model.py         Sample, Chunk, Track, Sidx, Segment, Layout
  tables.py        stts, stsc, stsz, stco, co64 parsers
  sample_table.py  chunk expansion and count/range consistency
  stbl.py          moov/trak/mdia/minf/stbl traversal
  sidx.py          sidx v0/v1 parse, encode, reference offsets
  layout.py        chunk-aligned segment planning
  validator.py     independent cross-check of sidx vs stbl
  rewrite.py       strip/reinsert sidx and shift chunk offsets
  rebuild.py       orchestration, probe sizing, atomic write
  fixture.py       deterministic acceptance fixtures
  cli.py           argument parsing, JSON output, exit codes
tests/
  test_rebuild.py  acceptance boundaries, round-trips, non-destructive writes
  test_invalid.py  damaged tables, encryption, exit codes
```
