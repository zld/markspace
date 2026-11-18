# media_index

Self-contained ISO BMFF (MP4) **sidx / stbl** parser, timeline rebuilder and
cross-validator for fragmented media delivered to an engine build pipeline.
Everything is implemented from scratch on bounded `memoryview` reads — no
playback frameworks, no external media tools, no network access.

## What it does

1. **Box layer** (`boxes.py`): parses box headers at arbitrary offsets with
   32-bit, 64-bit (`largesize`) and end-of-file (`size=0`) conventions, and
   reports truncated headers, out-of-range lengths and overflow attempts at
   absolute byte offsets.
2. **sidx layer** (`sidx.py`): version 0/1 `timescale`,
   `earliest_presentation_time`, `first_offset` and reference entries; media
   vs. nested-index references are resolved with a hard recursion depth limit
   and cycle detection.
3. **stbl layer** (`stbl.py`, `stbl_map.py`): `stsz`, `stz2` (4/8/16-bit),
   `stts`, signed/unsigned `ctts`, `stsc`, `stco`/`co64` and optional `stss`,
   including empty tables, chunk expansion and sample→byte-span mapping.
4. **Exact time** (`model.py`): all durations use integer numerator/denominator
   rational arithmetic — never floating point — and reject illegal timescales
   and runaway tick accumulation.
5. **Rebuild** (`rebuild.py`): maps sidx byte ranges to segment boundaries and
   sample index spans; adjacent segments may share a zero-width boundary,
   positive-length overlaps and boundary-crossing chunks are flagged.
6. **Validation** (`validate.py`): re-runs the parse layer and emits one result
   per check (`name`, `status`, `source_offset`, `reason`); a single corrupted
   item never aborts the remaining independent checks.
7. **CLI** (`cli.py`): `inspect` and `validate` subcommands with stable text
   and JSON output, sorted batch processing and exit codes
   `0` pass / `1` validation failure / `2` argument, file or structural error.

## Local setup (macOS Terminal, from the project root)

```bash
python3.11 -m venv .venv
.venv/bin/python -m pip install -e '.[test]'
.venv/bin/python -m pytest -q
.venv/bin/python -m media_index.cli validate --input .venv/generated/valid.mp4 --json
```

The first CLI run deterministically generates fixture files under
`.venv/generated/` (idempotent — existing files are never overwritten):

| file | contents |
| --- | --- |
| `valid.mp4` | 6 samples, 3 chunks, 3 aligned sidx references, ctts + stss |
| `nested.mp4` | top-level sidx pointing at a nested index sidx |
| `empty.mp4` | parseable file with empty sample tables |
| `bad_misaligned.mp4` | shifted sidx `first_offset` |
| `bad_oversized.mp4` | reference extending past end of file |
| `bad_duplicate_offset.mp4` | repeated stco chunk offset |
| `bad_truncated_field.mp4` | file chopped inside sidx entries |
| `bad_truncated_header.mp4` | trailing bytes that cannot form a box header |
| `bad_timescale.mp4` | zero sidx timescale |
| `bad_cycle.mp4` | index reference that points back at its own sidx |

## Commands

```bash
# Human-readable summary for one or more files (repeat --input for a batch)
.venv/bin/python -m media_index.cli inspect --input .venv/generated/valid.mp4

# Stable JSON validation report
.venv/bin/python -m media_index.cli validate --input .venv/generated/valid.mp4 --json

# Batch mode: paths are processed in sorted order; one bad file never blocks
# the others and the exit code is the worst individual result.
.venv/bin/python -m media_index.cli validate \
  --input .venv/generated/valid.mp4 \
  --input .venv/generated/bad_misaligned.mp4 --json

# (Re)generate only the deterministic fixtures
.venv/bin/python -m media_index.cli fixtures --gen-dir .venv/generated
```

## JSON contract

A validation report always contains:

- `sample_entries` — number of samples described by the size table
- `index_offset_checks.passed` / `.failed` — per-reference offset check counts
- `segment_boundaries` — rebuilt segments with `byte_start`, `byte_end`,
  `first_sample`, `last_sample`, `complete` and the sidx `source_offset`
- `checks` — every check with `name`, `status`, `source_offset`, `reason`,
  `details`
- `errors` — flat list of failed checks including their byte offsets

## Safety limits

Malformed inputs fail fast instead of allocating unbounded memory. The caps
live in `src/media_index/limits.py` (file size, box depth, sidx reference
count, nesting depth, table entry and chunk counts, tick accumulation). Files
are opened read-only and their contents are never executed.
