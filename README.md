# audioslice

A fully local tool for detecting silence regions and cutting usable speech /
ambience / performance segments out of long PCM WAV recordings. Results are
cached in SQLite so batch re-runs are cheap and reproducible.

## Install

```sh
python -m pip install numpy==1.26.4
python -m pip install -e .
```

## Demo

```sh
python scripts/make_fixture.py --out /tmp/audio-fixture.wav --seed 17
python -m audioslice.cli analyze /tmp/audio-fixture.wav \
    --window-ms 20 --noise-margin-db 8 --min-silence-ms 120 --json
```

## Tests

```sh
python -m unittest discover -s tests
```

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | All files processed successfully |
| 1 | Parameter or input error (bad WAV, missing path) |
| 2 | Batch run contained one or more failed files |
| 3 | SQLite read/write failure |

## Layout

- `audioslice.wav` — streaming PCM WAV reader with strict format validation
- `audioslice.calibration` — robust noise-floor estimate, dual thresholds
- `audioslice.detector` — RMS/peak windows, hysteresis, segmentation (no CLI/DB dependency)
- `audioslice.store` — versioned, parameter-keyed SQLite result cache
- `audioslice.batch` — recursive scheduling, dedup, failure-tolerant runs
- `audioslice.cli` — terminal entry point, JSON and human output
