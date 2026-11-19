# mixforge

Deterministic, fully local multi-track WAV mixing tool for game-audio level
pipelines. Gain automation drives one mixing graph shared by the offline
renderer and the blockwise streaming renderer, so both paths produce
bit-identical mixes.

## Install

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install -e '.[test]'
```

## Render

```sh
.venv/bin/python scripts/make_fixture.py --out fixtures
.venv/bin/python -m mixforge.cli render \
    --manifest fixtures/session.json --mode stream \
    --block-size 257 --out build/mix.wav --summary build/summary.json
```

## Test

```sh
.venv/bin/python -m unittest discover -s tests -v
```
