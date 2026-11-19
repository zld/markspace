# audiosplit

Offline, deterministic segmentation of long 16-bit PCM WAV recordings into
sample-accurate audio events, stored in SQLite and exportable as JSON Lines.

## Setup (macOS, Python 3.11)

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install -e '.[test]'
.venv/bin/python scripts/make_fixtures.py --out .tmp-fixtures
```

## Demo

```sh
.venv/bin/python -m audiosplit.cli scan .tmp-fixtures \
    --db .tmp/audio.db --frame-ms 10 --on-db 4 --off-db 2 \
    --min-silence-ms 120 --export .tmp/segments.jsonl
```

Expected summary: `files_ok=3`, `files_failed=0`, `segments=6`,
`silence_total_ms=3805`; exit code `0`.

Tests:

```sh
.venv/bin/python -m unittest discover -s tests
```

## Exit codes

| code | meaning |
| ---- | ------- |
| 0 | every input file was processed |
| 1 | at least one file failed (batch continued, error stored) |
| 2 | invalid CLI parameters, unreadable export target, or database error |

## How detection works

1. `wav.py` validates the RIFF structure (fmt/data chunks, 16-bit PCM, block
   alignment, byte rate, truncation) before any audio is read, then yields
   fixed-size blocks so memory stays flat for arbitrarily long files.
2. `detector.py` mixes channels to mono, computes RMS and peak per frame
   (`frame_ms`, 50 % overlap), estimates the noise floor from a percentile of
   recent quiet frames and freezes it while an event is active. Events need
   `attack_frames` consecutive frames above `noise + on_db`; they close after
   `hold_frames` of frames below `noise + off_db`.
3. `merge.py` adds `pre_roll`/`post_roll` guard samples, merges events whose
   gap is at most `min-silence-ms`, drops events shorter than
   `min-event-ms`, and re-reads the file to measure exact peak/RMS.
4. `store.py` writes one transaction per file (`runs`, `jobs`, `segments`).
   Re-running with the same path, file hash and parameters reuses the stored
   result unless `--force` is given.
5. `cli.py` handles batching, diagnostics, the `run_id=…/files_ok=…` summary,
   and JSON Lines export (`scan --export` or `audiosplit.cli export`).

All segment coordinates are sample indices with `start_sample` inclusive and
`end_sample` exclusive, and always satisfy
`0 <= start_sample < end_sample <= total_samples`.

## Fixtures

`scripts/make_fixtures.py` writes three deterministic WAVs (mono bursts at
48 kHz, stereo short pulses at 44.1 kHz, a slow fade plus an event that runs
into EOF) plus `invalid/` cases (empty, non-PCM, truncated). A non-recursive
scan therefore reports `files_ok=3`, while `invalid/` demonstrates the error
paths with `--recursive` (that batch reports `files_ok=3`, `files_failed=3`
and exits with status `1`).
