# mediacheck

Local, offline batch inspection for level recordings, cutscenes and voice
deliveries. It sniffs ISO BMFF (MP4) and RIFF/WAVE containers itself, extracts
track metadata, stores scan history in SQLite, packs delivery manifests and
compares a reference package against a target package using an explicit
policy. Nothing leaves the machine; media payload bytes are never decoded.

## Install

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install -e '.[test]'
```

Use `.venv/bin/python` for every command below.

## Quick start

```sh
.venv/bin/python scripts/make_samples.py --out .tmp/samples
.venv/bin/python -m mediacheck.cli scan .tmp/samples --db .tmp/media.sqlite --json .tmp/scan.json
.venv/bin/python -m mediacheck.cli pack .tmp/samples --out .tmp/manifest.json
.venv/bin/python -m mediacheck.cli compare .tmp/manifest.json .tmp/manifest.json \
    --duration-tolerance 0.02 --json .tmp/compare.json
.venv/bin/python -m mediacheck.cli report --db .tmp/media.sqlite
.venv/bin/python -m pytest
```

The sample set contains a WAV, two MP4s with real `moov/trak/mdia/stbl`
hierarchy and one truncated MP4 that the tool reports as a located failure.

## Commands

| Command | Purpose |
| --- | --- |
| `scan TARGET [--db DB] [--json OUT] [--no-recursive] [--av-tolerance S]` | Inspect a file or tree, optionally persist a batch and/or write JSON. |
| `pack TARGET --out MANIFEST.json [--label L] [--no-recursive]` | Build a delivery manifest with media and track summaries. |
| `compare REF TARGET [--policy P] [--duration-tolerance S] [--json OUT]` | Compare two manifests, directories, or `--ref-db`/`--target-db` batches. |
| `report --db DB [--batch ID] [--json OUT]` | Print a stored scan batch. |

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success, no hard inconsistencies. |
| 1 | Hard inconsistencies (duration drift, codec mismatch, missing/ambiguous tracks). |
| 2 | Usage, argument or policy error. |
| 3 | The batch had file read failures or a database/JSON write failure. |

File read and write failures take precedence over inconsistency findings.

## What is extracted

Per media: container, byte size, brands, movie timescale, track count, track
kinds, duration and codec summary. Per track: track id, kind, codec and raw
sample-entry fourcc, timescale, duration in units and seconds, start time (edit
lists included), language, dimensions or sample rate/channels/bit depth. Fields
that a format cannot supply are `null` and carry a reason in `reasons`.

## Comparison rules

1. Explicit `matching.pairs` (reference track_id -> target track_id) are
   applied first and may cross track kinds.
2. Remaining tracks pair by kind only when exactly one of each kind remains.
3. Two or more candidates on either side is reported as `ambiguous_tracks`; the
   tool never guesses. Missing, extra and unmatched tracks are all reported.
4. Durations are converted to seconds from each track's own timescale and empty
   edit-list offsets are folded into start time before any comparison.
   Tolerance is `max(tolerance_seconds, tolerance_ratio * reference_duration)`,
   with a small epsilon so floating-point rounding does not create failures.
5. Codec fields are compared only for fields that apply to the track kind.
   `warn_fields` downgrade mismatches to warnings, `ignore_fields` skip them,
   and `whitelist` entries may explicitly permit alternative values.

## Policy file

See `examples/delivery-policy.json`. Invalid policies report either
`line N column M` for JSON syntax errors or a dotted path such as
`matching.pairs[0].target_track_id` / `codec.whitelist.sounds`.

## Safety limits

Files above 2 GiB, more than 4096 boxes, nesting deeper than 12 levels, more
than 64 sample entries or 1024 edit entries are rejected with a located error.
Every read is bounds checked; an out-of-range box size is an error, never a
silent skip.

## Layout

```
src/mediacheck/sniff.py    container parsing and bounds checking
src/mediacheck/extract.py  normalized media/track summaries
src/mediacheck/pack.py     delivery manifest building/loading
src/mediacheck/compare.py  policy loading, matching, comparison
src/mediacheck/store.py    SQLite batches, media, tracks, failures
src/mediacheck/report.py   terminal and JSON rendering
src/mediacheck/cli.py      argument parsing and exit codes
scripts/make_samples.py    local fixture generation
tests/                     sniff, compare, store and CLI tests
```
