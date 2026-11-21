# mixforge

Local, deterministic timeline audio mixer. It reads a JSON project manifest
plus audio assets, unifies sample rates and channel layouts, applies
piecewise gain automation, sums tracks on a shared mixing graph, and writes
a WAV render together with a script-verifiable JSON statistics report.

Pure terminal tool; no GUI and no network access.

## Setup (macOS, inside `.venv`)

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install -e '.[test]'
```

## Demo

```sh
.venv/bin/python scripts/make_fixture.py --out /tmp/mixforge-fixture
.venv/bin/python -m mixforge render \
    --project /tmp/mixforge-fixture/project.json \
    --mode offline --block-frames 1024 \
    --report /tmp/mixforge-offline.json
.venv/bin/python -m mixforge render \
    --project /tmp/mixforge-fixture/project.json \
    --mode stream --block-frames 1024 \
    --report /tmp/mixforge-stream.json
.venv/bin/python -m pytest -q
```

Offline and stream modes share the same mixing graph and produce
bit-identical output; per-track and bus peak/RMS/clipping statistics match
within floating-point tolerance.

## Exit codes

| Code | Meaning |
| ---- | ------- |
| 0 | completed; `error` clip policy not triggered |
| 2 | invalid manifest or command-line parameters |
| 3 | unreadable asset or unwritable output/report |
| 4 | `error` clip policy enabled and clipping detected |

## Manifest fields

- `project_rate`, `frames`, `channels`: output timeline definition.
- `clip_policy`: `preserve`, `hard_limit` or `error`; `clip_threshold` is the
  linear level (default 1.0) above which scalar samples count as clips.
- `automation_interval`: spacing in seconds of the curve sample summary.
- `tracks[]`: `name` (unique), `file`, `start_frame` (>= 0), `gain_db`,
  `layout` (`mono_copy`, `stereo`, or explicit `matrix`), optional sorted
  `automation` points (`linear`/`hold`, unique times) and `mutes` intervals.

Safety limits (rejected with an explicit field error): tracks <= 64,
channels <= 64, project rate 8 kHz–384 kHz, frames <= two hours at the max
rate, block frames 64–1,048,576.

## Module layout

```
src/mixforge/
  errors.py      exit codes and typed failures
  project.py     manifest parsing and validation
  assets.py      audio reading and channel-layout normalization
  resample.py    windowed-sinc sample-rate conversion
  automation.py  gain curves, mute segments, curve summaries
  mixer.py       track nodes and the shared mixing graph
  render.py      offline and streaming render paths, atomic writes
  metrics.py     streaming peak/RMS/clip accumulators
  report.py      JSON report and terminal summary
  cli.py         argument parsing and exit-code aggregation
scripts/make_fixture.py   deterministic synthetic fixture generator
tests/test_pipeline.py   processing-chain coverage
```
