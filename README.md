# audiosplit

Local, deterministic WAV event segmentation for sound-effect libraries.
Everything runs offline against local PCM WAV files; results are stored in
SQLite and can be exported as WAV events plus JSON manifests.

## Quick start

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install -e .
.venv/bin/python scripts/make_samples.py --out .tmp/samples
.venv/bin/python -m audiosplit.cli batch \
  --input .tmp/samples --db .tmp/audio.sqlite --export .tmp/export \
  --frame-ms 10 --noise-percentile 20 --margin-db 8 --merge-ms 35
.venv/bin/python -m unittest discover -s tests
```

## Detection model

- Frames are non-overlapping windows; RMS and peak envelopes are computed with
  NumPy.
- A sliding percentile over a configurable window estimates the local noise
  floor, so quiet passages and drifting device noise do not need a fixed dBFS
  threshold.
- Onsets need `--onset-frames` consecutive frames above the high threshold
  (noise floor + `--margin-db`). The start is then backtracked through the low
  threshold (high minus `--hysteresis-db`) to keep slow attacks.
- Tails continue through the low threshold and are extended by
  `--release-frames`.
- Events separated by fewer than `--merge-ms` are merged. Events shorter than
  `--min-segment-ms` or below `--min-peak-dbfs` are filtered and their span is
  counted as silence.
- Kept segments and silence ranges exactly partition the input from sample 0 to
  the final sample.

## Commands and exit codes

`detect` processes one WAV file; `batch` accepts a file or a directory tree
(recursive, `.wav` only, sorted by relative path, symlinks skipped).

- `0` all files succeeded
- `1` one or more files failed (processing continued)
- `2` invalid parameters, no inputs, or a database error

Runs are recorded per input with a SHA-256 fingerprint and the canonical config
digest. Re-running the same input and config skips it by default; pass
`--on-duplicate new` to record another comparison run.

## Layout

```
src/audiosplit/config.py    validated detection parameters
src/audiosplit/audio_io.py  WAV parsing, mono normalization, fingerprints
src/audiosplit/detector.py  framing, noise floor, hysteresis
src/audiosplit/segments.py  fades, minimum length, silence coverage
src/audiosplit/store.py     SQLite runs, segments, silence ranges
src/audiosplit/batch.py     discovery, scheduling, failure isolation
src/audiosplit/exporter.py  atomic WAV and JSON export
src/audiosplit/cli.py       argument parsing and terminal summary
tests/                      end-to-end and edge-case tests
scripts/make_samples.py     deterministic sample generator
```
