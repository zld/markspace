# LadderForge

A local, terminal-only tool that scans encoding combinations over generated
sample footage and turns the measurements into a monotonic bitrate ladder.

It exists to replace hand-tuned ladders with something reproducible: the same
source, parameters and FFmpeg version always produce the same task keys, so an
interrupted scan resumes instead of re-running finished work, and a changed
source or parameter set is refused rather than silently mixed in.

## Requirements

* macOS with Homebrew
* Python 3.11, NumPy 2.2.6 (installed into the project venv)
* FFmpeg 7.1+ with `libx264`, plus `ffprobe`

```sh
brew install python@3.11 ffmpeg
python3.11 -m venv .venv
.venv/bin/python -m pip install numpy==2.2.6
```

## Demo

```sh
bash scripts/demo.sh
```

Or run the two acceptance commands directly:

```sh
PYTHONPYCACHEPREFIX=/tmp/ladderforge-pycache .venv/bin/python -m compileall -q src tests
PYTHONPYCACHEPREFIX=/tmp/ladderforge-pycache PYTHONPATH=src .venv/bin/python -m unittest discover -s tests

PYTHONPATH=src .venv/bin/python -m ladderforge run --db .runs/demo.sqlite \
  --frames 48 --resolutions 426x240,640x360 \
  --bitrates-kbps 250,500,1000,1000 --presets ultrafast,medium \
  --workers 2 --limit-jobs 3

PYTHONPATH=src .venv/bin/python -m ladderforge run --db .runs/demo.sqlite \
  --frames 48 --resolutions 426x240,640x360 \
  --bitrates-kbps 250,500,1000,1000 --presets ultrafast,medium \
  --workers 2 --resume
```

The first pass prints `jobs_total=12 completed=3 pending=9 ... state=PARTIAL`.
The resume prints `completed=12 ... reused=3 ... state=COMPLETE` and three
rungs, each with `target_bps`, `actual_bps`, `quality_psnr_db`, `aliases` and
`monotonicity=PASS`.

## How it fits together

| Module | Responsibility |
| --- | --- |
| `plan.py` | Normalise resolutions, presets and bitrates; collapse duplicate targets into aliases; derive replay-safe task keys |
| `synth.py` | Generate deterministic moving-texture yuv420p samples with NumPy |
| `encode.py` | Run FFmpeg via argument arrays, writing to unique partial files |
| `measure.py` | Probe real bitrate from bytes/duration; compute luma PSNR |
| `store.py` | SQLite schema, WAL, transactions, leases and state transitions |
| `scheduler.py` | Concurrent claim/renew/release loop, pause/stop, failure isolation |
| `ladder.py` | Select one candidate per target under monotonic constraints |
| `report.py` | Terminal summary and atomic JSON report |
| `__main__.py` | CLI parsing, orchestration and exit codes |

Data flows from `plan` into `synth`, `scheduler`, `encode` and `measure`; each
measured job is committed to `store`, and `ladder`/`report` read back the
completed rows.

## Behaviour worth knowing

* **Task keys** hash the source bytes, normalised parameters, frame geometry and
  FFmpeg version. A changed source or parameter set yields new keys, so old
  results are never silently reused; `--resume` refuses when the recorded plan
  signature no longer matches.
* **Leases**: a worker claims a task with `BEGIN IMMEDIATE`, renews it via a
  heartbeat, and releases it back to `pending` on interruption. Crash-killed
  leases expire and are reclaimed by the next run.
* **Failures** are recorded per job with an error summary; other jobs continue.
  Failed jobs are only re-run with `--retry-failed`.
* **Duplicates**: `--bitrates-kbps 250,500,1000,1000` encodes three targets, and
  the ladder reports `duplicate_targets=1` with `aliases=1` on that rung.
* **Monotonicity** means actual bitrate does not fall and PSNR does not fall as
  the target rises. Adjacent targets may share one encode; each still keeps its
  own rung.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Complete, or a controlled pause (SIGTERM / `--limit-jobs` slice) |
| 1 | Configuration or scan failure |
| 2 | Ladder could not be made monotonic |
| 130 | Interrupted with SIGINT |

## Tests

```sh
PYTHONPYCACHEPREFIX=/tmp/ladderforge-pycache PYTHONPATH=src \
  .venv/bin/python -m unittest discover -s tests
```

The suite generates its own samples and skips encoder-dependent tests if
FFmpeg/libx264 is unavailable.
