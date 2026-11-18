# audiomix

Local, deterministic multi-track WAV mixing for offline masters and
streaming chunked previews. Offline and streaming renders share one mix
graph and one block iterator, so their output is bit-identical regardless
of block size.

## Requirements

- Python 3.11, NumPy 1.26, soundfile 0.12+ (PCM WAV only)
- Everything runs locally; no network services, real-time devices or
  compressed-format decoding.

## Install

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install -e '.[test]'
```

## Generate fixtures and render

```sh
.venv/bin/python scripts/make_fixtures.py --out .venv/fixtures

# Streaming render, 257-frame blocks, with statistics and automation probes
.venv/bin/python -m audiomix.cli render \
  --manifest .venv/fixtures/session.json \
  --out .venv/mix.wav --rate 48000 --block-frames 257 \
  --report .venv/report.json --sample-at 0.5,1.0

# Offline render (same iterator, one timeline-spanning block)
.venv/bin/python -m audiomix.cli render \
  --manifest .venv/fixtures/session.json \
  --out .venv/offline.wav --mode offline --report .venv/offline.json
```

Both WAVs have identical frame counts and match sample-for-sample
(max abs diff `0.0`, tolerance `1e-6`).

## Exit codes

| Code | Meaning |
| ---- | ------- |
| 0    | success |
| 2    | manifest or argument error (field and track located) |
| 3    | input/output or render failure (including NaN/Inf aborts) |

Errors are emitted to stderr as single-line JSON, e.g.
`{"error": "...", "field": "start", "track": "voice"}`. Failed renders
remove the temporary `.part` file so no half-written output remains.

## Architecture

```
model.py      domain types + manifest validation (located errors)
resample.py   streaming linear resampler (rational phase, chunk-exact)
              and channel layout normalization
automation.py linear / dB gain curves, -inf dB and endpoint extension
mixer.py      fixed-order per-track node chain + bus gain (shared graph)
render.py     streaming track players, BlockRenderer, atomic WAV write
report.py     streaming peak/RMS/active-frame/clipping stats + JSON
cli.py        argument parsing, exit codes, terminal summary
```

The per-track node order is fixed: channel mapping -> mute -> track gain
-> automation -> bus sum -> bus gain. NaN/Inf values are located to the
owning track and abort the render instead of being written.

## Tests

```sh
.venv/bin/python -m pytest
```

Memory is bounded by block size and the 8192-frame read chunk, not by
timeline length: four 5-minute stereo tracks render with a few MB of
Python heap regardless of total frame count.
