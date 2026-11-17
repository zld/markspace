# trackmix

Deterministic, fully local command-line audio mixing tool for indie game
audio pipelines. It mixes dialog, music and ambient WAV tracks with
different sample rates / channel counts onto one master timeline, applies
dB gain automation with absolute-frame addressing, and renders the same
mixing graph both offline (whole timeline) and as streaming blocks.

## Install

```sh
brew install python@3.11
"$(brew --prefix python@3.11)/bin/python3.11" -m venv .venv
.venv/bin/python -m pip install -e .
```

## Demo

```sh
.venv/bin/python -m trackmix demo --seed 23 --tracks 4 --frames 96000 \
    --rate 48000 --block-frames 257 --automation-step 12000 \
    --stress-clip --verify-stream
```

## Tests

```sh
.venv/bin/python -m unittest discover -s tests
```

## Exit codes

| Code | Meaning |
| ---- | ------- |
| 0 | success |
| 2 | parameter / manifest error |
| 3 | audio read / resample error |
| 4 | output write failure |
| 5 | offline/streaming verification failure |
