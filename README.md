# abrscan

Deterministic, fully local ABR ladder scanner for comparing encoding parameters on
program-generated sample clips. The encoder is a **block-transform proxy** (DCT +
quantisation + zlib) used for parameter comparison only; it is not a production
video codec and makes no compatibility claims.

## Setup

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install numpy==2.2.6
```

## Usage

```sh
mkdir -p .runs

# Interrupt after 6 measurements (exit 75)
PYTHONPATH=src .venv/bin/python -m abrscan run \
  --db .runs/demo.sqlite --fixture moving-texture \
  --targets-kbps 80,160,320,320,640 --workers 1 --stop-after 6

# Resume the same database with two workers (exit 0)
PYTHONPATH=src .venv/bin/python -m abrscan run \
  --db .runs/demo.sqlite --fixture moving-texture \
  --targets-kbps 80,160,320,320,640 --workers 2

# Print the ladder and monotonicity checks (exit 0)
PYTHONPATH=src .venv/bin/python -m abrscan verify --db .runs/demo.sqlite
```

## Tests

```sh
PYTHONPATH=src .venv/bin/python -m unittest discover -s tests -v
```

## Exit codes

| Code | Meaning |
| ---- | ------- |
| 0    | Scan complete / ladder valid |
| 2    | Invalid parameters (including database paths outside the workspace) |
| 3    | No feasible ladder, database error, or unfinished scan |
| 75   | Intentionally interrupted by `--stop-after` |
| 130  | Interrupted by SIGINT |

## Layout

- `src/abrscan/fixture.py` — deterministic clips and content hash
- `src/abrscan/codec.py` — block transform, quantisation, compression, decoding
- `src/abrscan/metrics.py` — encoded bytes, actual bitrate, PSNR
- `src/abrscan/db.py`, `tasks.py`, `records.py`, `store.py` — SQLite schema,
  atomic claims, leases, resume
- `src/abrscan/scheduler.py` — concurrent workers and interruption
- `src/abrscan/ladder.py` — Pareto candidates, target selection, monotonicity
- `src/abrscan/params.py`, `cli.py`, `__main__.py` — validation and terminal UI

All data stays inside the workspace; no network access or external media is used.
