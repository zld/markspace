# mixforge

Deterministic offline/streaming mixdown for game-audio deliverables. Tracks
from different recording and export pipelines are resampled, placed on a
timeline, shaped by automation, summed onto buses and rendered as a float
master — with per-track levels and a JSON report that localises any problem.

Everything runs locally. The tool never touches the network, reads only files
inside the manifest's directory, and never overwrites inputs.

## Quick start

```sh
brew install libsndfile
python3.11 -m venv .venv
.venv/bin/python -m pip install -e '.[test]'
.venv/bin/python scripts/make_fixtures.py --out .tmp/audio
.venv/bin/python -m mixforge.cli render \
  --manifest .tmp/audio/session.json \
  --rate 48000 --block-frames 257 \
  --output .tmp/mix.wav --report .tmp/report.json
```

Expected terminal output:

```
tracks=3 rate=48000 frames=96000
track=dialogue peak=0.236029 rms=0.057225 automation_samples=11 clip_count=0
track=music peak=0.175687 rms=0.037132 automation_samples=11 clip_count=0
track=ambience peak=0.383525 rms=0.022257 automation_samples=11 clip_count=0
master peak=0.298749 rms=0.060787 clip_count=0
blocks=374 block_frames=257 last_block_frames=139
offline_stream_max_error=0.000e+00
```

Run the tests with `.venv/bin/python -m pytest`.

## Manifest

```json
{
  "output_rate": 48000,
  "duration_ms": 2000,
  "buses": {
    "music": {
      "gain_db": -2.5,
      "automation": [
        {"ms": 0, "value": -9.0, "unit": "db"},
        {"ms": 600, "value": -2.5, "unit": "db"}
      ]
    }
  },
  "tracks": [
    {
      "id": "dialogue",
      "path": "dialogue.wav",
      "offset_ms": 0,
      "channels": "mono",
      "gain_db": 3.0,
      "bus": "dialogue",
      "automation": [
        {"ms": 0, "value": -18.0, "unit": "db"},
        {"ms": 250, "value": 0.0}
      ]
    }
  ]
}
```

Track fields:

| field | required | meaning |
| --- | --- | --- |
| `id` | yes | unique, no path separators |
| `path` | yes | relative to the manifest; must stay inside its directory |
| `offset_ms` | no | timeline position, default 0 |
| `channels` | no | `"mono"`, `"stereo"` or a 2-row matrix; omitted = source layout |
| `gain_db` | no | static track gain, default 0 |
| `bus` | no | bus id, or `master` (default) |
| `automation` | no | ≥2 points of `{ms, value, unit}` |

Automation points are piecewise linear in gain (dB values are converted per
node), must be strictly increasing, and hold their endpoint values outside
the curve. The report lists each curve sampled at every knot plus the session
endpoints, so the gain decisions can be reviewed.

A channel matrix is two rows (outputs) by *N* columns (source channels). Its
width must equal the source channel count: a mismatch is an error, never a
silent channel drop.

## Exit codes

| code | meaning |
| --- | --- |
| 0 | success |
| 2 | manifest or argument error |
| 3 | media, resampling or output-write failure |
| 4 | offline and streaming renders disagree (≥ 1e-6) |

On failure the CLI writes `{"status": "error", "error": {...}}` to the
`--report` path when one was given, naming the track and field.

## Design notes

- **One graph, two schedules.** `MixGraph.render_block(start, frames)` is a
  pure function of absolute frame positions. The offline pass is one block;
  streaming only changes the partition. Gain is evaluated from absolute frame
  numbers, so automation cannot reset at a block boundary.
- **Bit-identical streaming.** Resampling and channel mixing are written as
  position-based, fixed-order arithmetic rather than BLAS calls, so a block
  partition does not change a single sample. The comparison still runs and
  reports `offline_stream_max_error` and the first differing sample.
- **Deterministic resampling.** Kaiser-windowed sinc interpolation evaluated at
  exact fractional source positions; same input gives the same output.
- **Metrics.** Peak, RMS (per sample across channels) and clip count, where a
  clip is `|amplitude| >= 1.0`. Counts are additive, so block-level and
  whole-session statistics agree exactly.
- **Safe writes.** Outputs go to a temporary file and are renamed on success;
  a partial file is never reported as success. Inputs are protected from
  overwrite.

## Layout

```
src/mixforge/
  cli.py         argument parsing, orchestration, terminal output
  manifest.py    manifest validation
  automation.py  curve parsing, evaluation and sampling
  resample.py    rate conversion and channel layout
  audio_io.py    media probing, decoding, atomic writes
  graph.py       tracks -> buses -> master mix graph
  engine.py      offline and streaming renderers, comparison
  metrics.py     peak, RMS and clipping statistics
scripts/make_fixtures.py   deterministic demo session
tests/                     pytest suite
```
