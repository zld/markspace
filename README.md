# audiosplit

Offline, reproducible PCM WAV segmentation for game-audio pipelines. It cuts
long recordings into sample-accurate sound segments and silence regions,
persists every run with its parameters in a local SQLite database, and exports
coordinates as CSV or JSON for engine event tables and asset retrieval.

## Design constraints

- PCM WAV only (8/16/24/32-bit, mono up to 8 channels); no decoding,
  resampling or audio-device access.
- Adaptive detection: a fixed dB threshold misses weak events and clips tails,
  so the detector estimates a per-frame noise floor and opens/closes segments
  with hysteresis.
- All boundaries are half-open integer sample intervals `[start, end)`.
- Pure local operation; the only third-party dependency is NumPy.

## Setup

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install numpy==1.26.4
```

## Acceptance demo

```sh
.venv/bin/python scripts/make_samples.py --out ./.sample
PYTHONPATH=src .venv/bin/python -m audiosplit.cli batch \
  --input ./.sample --db ./results.sqlite --export ./segments.json \
  --frame-ms 10 --noise-margin-db 8 --min-silence-ms 120
```

Expected terminal summary: `files_ok` 3, `files_failed` 1, `segments` ~5,
`silence_total_ms` inside 1500–2500 ms. Exit code is 1 because the generated
fixture set contains one deliberately truncated file.

## Commands

| Command | Purpose |
| --- | --- |
| `init-samples --out DIR` | Generate deterministic sample WAVs and one corrupt file |
| `detect --input FILE --db DB` | Analyse one file, persist the result |
| `batch --input DIR --db DB [--export FILE]` | Recursive batch with stable relative-path ordering |
| `export --db DB --output FILE [--run-id N]` | Export a run (`.json` or `.csv`); defaults to latest run |

Detector flags: `--frame-ms`, `--noise-margin-db`, `--min-silence-ms`,
`--min-segment-ms`, `--pad-ms`. Values outside the configured ranges are
rejected.

Exit codes: `0` all files succeeded, `1` at least one file failed,
`2` parameter or global input error.

## Detection pipeline

1. **WAV validation** (`wav.py`) — rejects empty files, truncated frames,
   non-PCM formats and unsupported sample widths/channel counts; normalises to
   float32 in `[-1, 1]`.
2. **Framing** (`detector.py`) — non-overlapping frames, multi-channel
   mean-square energy converted to dB.
3. **Noise floor** — downward-only tracker combined with a sliding-window
   minimum, capped at the file-wide 10th percentile so a quiet lead-in cannot
   anchor thresholds and ordinary noise gaps are not mistaken for events.
4. **Hysteresis state machine** — opens above the high threshold (noise floor
   + margin), closes below the low threshold (3 dB lower), with fade-in and
   fade-out backtracking so slow ramps and weak adjacent events are retained.
5. **Merging & shaping** — gaps shorter than `min-silence-ms` merge adjacent
   runs; runs below `min-segment-ms` are dropped; surviving segments receive
   symmetric padding clamped to file bounds.
6. **Persistence** (`store.py`) — one transaction per run, per-file savepoint,
   foreign keys on, parameterised queries. A failed file never rolls back
   successful records. Re-imports are distinguished by content SHA-256 and the
   parameter version.
7. **Export** (`export.py`) — written to a temp file then atomically renamed;
   coordinates come straight from the database so exports and stored records
   cannot diverge.

## Tests

```sh
.venv/bin/python -m pip install pytest
.venv/bin/python -m pytest
```

The detector processes mono 48 kHz audio well above the 5x realtime target.
