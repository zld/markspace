# audiopipe

Offline WAV batch pipeline for a small game studio: resample every clip to
one sample rate with a **self-implemented polyphase FIR kernel**, then
loudness-normalise it, and leave a JSON report detailed enough to
recompute every number offline.

Pure local Python — no services, no GUI, no audio frameworks beyond
NumPy and SoundFile/libsndfile for file IO.

## Requirements

* macOS (tested on Darwin 27 / Python 3.11.18)
* `python3.11`
* project-local virtual environment in `.venv/` (never the system Python)

## Quick start (exactly the acceptance flow)

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install --upgrade pip
.venv/bin/python -m pip install numpy==1.26.4 soundfile==0.12.1
.venv/bin/python scripts/make_fixtures.py --out .tmp/input
.venv/bin/python -m unittest discover -s tests -v
.venv/bin/python -m audiopipe.cli normalize \
    --input .tmp/input --output .tmp/output --report .tmp/report.json \
    --rate 48000 --target-lufs -18 --max-gain-db 12 --format PCM_24 --jobs 2
```

The batch prints, for example:

```
files=3 success=3 failed=0
  ambience_48000_stereo.wav: rate=48000 channels=2 frames=48000 padding=0 rms=0.0912 lufs=-18.00 gain_db=5.04 (target)
  dialogue_44100_mono.wav: rate=48000 channels=1 frames=57600 padding=96 rms=0.1397 lufs=-18.00 gain_db=0.44 (target)
  music_22050_6ch.wav: rate=48000 channels=6 frames=48000 padding=96 rms=0.0570 lufs=-18.00 gain_db=-8.33 (target)
report: .tmp/report.json
```

`python -m audiopipe.cli` works from the repository root because the
`audiopipe` symlink next to `pyproject.toml` mirrors `src/audiopipe`, so
no editable install or `PYTHONPATH` setup is needed.

## Verify a report offline

```sh
.venv/bin/python -m audiopipe.cli verify --report .tmp/report.json
# verified=3 mismatched=0 skipped=0
```

`verify` re-reads each output WAV and recomputes sample rate, channel
count, frame count, RMS, peak, K-weighted LUFS and rail-clipping count,
comparing them with the JSON record:

| check                  | tolerance                       |
| ---------------------- | ------------------------------- |
| `sample_rate`          | exact                           |
| `channels`             | exact (and 1..8)                |
| `frames`               | exact                           |
| `after.rms`            | `max(1e-9, 1e-6 * value)`       |
| `after.peak`           | `1e-9`                          |
| `after.lufs`           | `1e-4`                          |
| `after.clipped_samples`| exact                           |

## Options

| flag | default | notes |
| ---- | ------- | ----- |
| `--rate` | required | output rate, 8–384 kHz |
| `--target-lufs` | `-18` | accepted range `-70 … 0` |
| `--max-gain-db` | `12` | finite and non-negative |
| `--max-peak-db` | `-0.1` | ≤ 0 dBFS; the linear ceiling is capped just below full scale |
| `--format` | `PCM_24` | `PCM_16` or `PCM_24` |
| `--jobs` | `1` | worker processes |
| `--block-frames` | `65536` | streaming block size, 1–1048576 |

## Exit codes

| code | meaning                                                     |
| ---- | ----------------------------------------------------------- |
| 0    | every file processed successfully                            |
| 1    | at least one file failed; the report was still written       |
| 2    | invalid arguments or batch-level configuration error         |
| 3    | the report could not be written                              |

## What the pipeline does

1. **Scan.** Every `.wav` under `--input` is discovered recursively and
   mapped to the same POSIX relative path under `--output`.  Identical or
   nested input/output trees are refused, as is a report path inside the
   input tree.
2. **Read.** Only uncompressed PCM WAV (16/24/32-bit) with 1–8 channels
   and 8–384 kHz is accepted.  Everything else becomes a per-file error
   record.  Blocks are `frames × channels` float64; the channel axis is
   never treated as time.
3. **Resample.** A self-implemented polyphase windowed-sinc FIR kernel
   (see below) converts to `--rate`.  The frame count is
   `floor(frames * L / M)` and zero-padded convolution tails are counted
   and reported.
4. **Measure.** RMS (per channel and overall), peak, K-weighted LUFS and
   clipping are computed on the resampled programme.
5. **Plan gain.** `target - measured` LUFS, capped by `--max-gain-db`,
   then rolled back if the predicted peak would cross the peak ceiling
   (`--max-peak-db`, default −0.1 dBFS).  The final gain and the limiting
   reason are recorded.
6. **Write.** Gain is applied, samples are quantised on the integer PCM
   grid and written through a temporary file that is renamed only on
   success — a failed job leaves no output and no `.part` file.
7. **Report.** Deterministic JSON with strictly finite numbers
   (`allow_nan=False`), fixed decimal precision, and one record per file.

Each file is read and resampled twice (measure, then measure-and-write),
so memory stays at one block plus filter history regardless of file
length.  Files are processed sequentially (`--jobs 1`) or in a process
pool (`--jobs N`); a failure in one file never affects another.

### Resampling kernel

`src/audiopipe/resample.py`, no external resampler involved:

* ratio reduced to coprime `L / M` with `gcd(in_rate, out_rate)`;
* `K = 96 * ceil(M / L)` taps per phase (96 taps per phase at the output
  rate at any ratio), prototype length `K·L + 1`;
* Kaiser window, β = 8.6;
* cutoff at 93 % of the lower Nyquist frequency — under 1 dB droop below
  0.9·Nyquist, > 55 dB rejection from 0.98·Nyquist and > 90 dB beyond;
* every polyphase branch normalised to unit DC gain;
* odd, exactly centred prototype — output grid aligned with input time
  zero, **no fractional delay**;
* out-of-range taps read zeros, so empty, single-frame and sub-filter
  inputs behave deterministically.

The batch path uses this one kernel for every file, and its output is
bit-identical regardless of chunk boundaries.

### Measurement definitions

* **RMS** — root mean square over all samples of all channels.
* **LUFS** — `-0.691 + 10·log10(Σ mean(y²))` where `y` is the signal after
  the BS.1770 K-weighting cascade (high shelf +4 dB @ 1682 Hz, high-pass
  @ 38 Hz).  The coefficients are designed from the analog prototype with
  a bilinear transform, so the same code is correct at any sample rate;
  at 48 kHz it tracks the published table within 0.001 dB.  The ungated
  formula is used (documented as an approximation) for both before and
  after, so the two are directly comparable.  On steady material it
  matches a gated BS.1770 measurement exactly; on programmes with long
  quiet passages the missing relative gate reads a few LU low (the demo
  ambience and music clips differ from gated by about 0.5 LU), which is
  the known limitation of the approximation.  Silence reports
  `lufs: null`.
* **Clipping — four distinct quantities, never merged:**
  * `input.rail_clipped_samples` — source samples pinned to a PCM rail
    (clipping already present in the input material);
  * `before.clipped_samples` / `resample.overshoot_samples` — the
    resampled programme above full scale before any gain (interpolation
    overshoot);
  * `output.out_of_range_samples` — post-gain samples outside `[-1, 1]`
    immediately before quantisation;
  * `after.clipped_samples` — samples pinned to an output PCM rail, i.e.
    magnitude actually lost to the fixed-point grid.

## Report shape

```jsonc
{
  "summary": {"files": 3, "success": 3, "failed": 0},
  "files": [
    {
      "relative_path": "dialogue_44100_mono.wav",
      "status": "ok",
      "input":  {"sample_rate": 44100, "channels": 1, "frames": 52920,
                 "subtype": "PCM_16", "rail_clipped_samples": 0},
      "output": {"path": "dialogue_44100_mono.wav", "sample_rate": 48000,
                 "channels": 1, "frames": 57600, "subtype": "PCM_24",
                 "out_of_range_samples": 0},
      "padding_frames": 96,
      "resample": {"numerator": 160, "denominator": 147,
                   "input_frames": 52920, "output_frames": 57600,
                   "padding_left_frames": 48, "padding_right_frames": 48,
                   "overshoot_samples": 0,
                   "filter": {"taps_per_phase": 96, "filter_length": 15361,
                              "window": "kaiser", "window_beta": 8.6,
                              "cutoff_hz": 21168.0, "dc_gain": 1.0}},
      "before": {"rms": 0.1328, "lufs": -18.49, "peak": 0.350,
                 "clipped_samples": 0, "frames": 57600},
      "after":  {"rms": 0.1404, "lufs": -18.00, "peak": 0.370,
                 "clipped_samples": 0, "frames": 57600},
      "gain":   {"applied_gain_db": 0.49, "requested_gain_db": 0.49,
                 "linear_gain": 1.058, "max_gain_limited": false,
                 "peak_limited": false, "reason": "target"},
      "error": null
    }
  ]
}
```

Failed files have `"status": "error"` and
`"error": {"code": "...", "message": "..."}` with codes
`unreadable`, `unsupported_format`, `invalid_sample_rate`,
`unsupported_channels`, `non_finite_input`, `write_failed`,
`non_finite_output`, `internal_error` (and `worker_failed` if a pool
worker dies).  An input sitting on a PCM rail is *not* an error: it is
counted and processed normally.

## Layout

```
pyproject.toml           project metadata + pinned dependencies
audiopipe -> src/audiopipe   import shim for `python -m audiopipe.cli`
src/audiopipe/
  __init__.py            package boundary
  cli.py                 argparse, exit-code mapping
  batch.py               scan, schedule, per-file processing, verify
  resample.py            polyphase FIR kernel
  loudness.py            RMS / K-weighted LUFS / gain planning
  io.py                  PCM WAV probing, block reader, atomic writer
  report.py              strict finite JSON serialisation
tests/
  test_pipeline.py       end-to-end, boundary and failure recovery
  test_resample.py       kernel contract
  test_loudness.py       meter and gain planning
  test_io.py             probing, quantisation, atomic writes
  support.py             shared helpers
scripts/make_fixtures.py deterministic fixture generator
```

## Fixture sets

```sh
.venv/bin/python scripts/make_fixtures.py --out .tmp/input           # demo: 3 files
.venv/bin/python scripts/make_fixtures.py --out .tmp/edge --set all  # + edge/rejection cases
```

`--set all` adds zero-frame, single-frame, sub-filter-length, 8-channel,
clipped-square and nested files, plus rejected inputs (9 channels, float
encoding, 4 kHz rate, corrupt header).  Fixtures are generated purely
from code with a fixed seed and are byte-identical across runs.
