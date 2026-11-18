# audioslice

Local, dependency-light tool for cutting mono PCM voice/environment recordings
into segments suitable for subtitle alignment and event annotation. It handles
HVAC background noise, fades in/out and short inter-sentence pauses using an
adaptive noise-floor threshold with hysteresis, and records every result in a
reproducible SQLite database.

No audio devices, network access or external services are required.

## Requirements

- macOS (Darwin), Python 3.11
- NumPy 1.26.4, installed only inside the project-local `.venv`

## Setup

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install numpy==1.26.4
.venv/bin/python -m pip install -e .
```

The editable install registers the `audioslice` package for the
`python -m audioslice.cli` entry point used by all commands.

## Acceptance demo

```sh
.venv/bin/python -m audioslice.cli demo --db ./demo.sqlite3 --out ./demo-fixtures --seed 42
.venv/bin/python -m audioslice.cli batch ./demo-fixtures --db ./demo.sqlite3 \
  --threshold-db -38 --min-silence-ms 180 --merge-gap-ms 90 --json
```

With the fixed seed this processes three files, fails zero, yields a
single-digit-to-teens segment count and seconds of total silence. Every
boundary is an integer sample on a half-open `[start, end)` interval.

Or run the full chained flow: `bash scripts/demo.sh`.

## Commands

| Command | Purpose |
| --- | --- |
| `demo` | Generate three deterministic synthetic WAV fixtures and initialise the DB |
| `detect WAV` | Analyse one file, store and print its segments/silence zones |
| `batch DIR` | Process every `*.wav` directly in `DIR` in stable filename order |
| `list` | List stored results as JSON (`--task` filters by id or exact path) |
| `summary` | Aggregate counts and silence totals as JSON |

Common detection flags: `--frame-ms`, `--hop-ms`, `--threshold-db`,
`--no-adaptive`, `--noise-percentile`, `--noise-margin-db`,
`--hysteresis-db`, `--confirm-ms`, `--min-silence-ms`, `--merge-gap-ms`.

### Detection semantics

- **Adaptive threshold (default):** the close threshold is the recording's own
  noise floor (low percentile of the frame-dB envelope) plus a guaranteed
  `--noise-margin-db`. A single burst cannot lift this low-tail estimate.
- **Fixed mode (`--no-adaptive`):** uses `--threshold-db` directly.
- **Hysteresis:** opening needs `--confirm-ms` of consecutive frames above the
  upper threshold; closing needs `--min-silence-ms` below the lower threshold,
  so fades and brief dips do not fragment an utterance.
- **Merging:** adjacent raw segments separated by less than `--merge-gap-ms`
  are merged for reporting, while raw boundaries and exact sample-accurate
  silence totals (the complement of raw speech) are retained.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | All files succeeded |
| 1 | Batch completed but one or more files failed |
| 2 | Invalid arguments or local environment error |
| 3 | Database missing/unavailable, corrupt or schema-incompatible |

## Reproducibility and resume

Inputs are fingerprinted by SHA-256 and parameter sets by a hash of their full
JSON. Re-running the same input with the same parameters reuses the stored
result; changed parameters create new result rows. Each file result is written
inside one SQLite transaction, so an interrupted process never leaves a partial
result, and re-running simply resumes from cached files.

## Tests

```sh
.venv/bin/python -m unittest discover -s tests
```
