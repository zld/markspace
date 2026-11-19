# audiopipe

Deterministic, re-computable audio preparation for a game audio pipeline:
resample PCM WAV assets to one engine sample rate and normalise loudness to a
target LUFS under a peak ceiling, recording every parameter and statistic in
JSON.

The resampling kernel (polyphase windowed-sinc, phase accumulator, mirror
boundary handling) and the K-weighting filters are implemented in this project.
`soundfile` is used only for WAV container I/O and NumPy only for array
arithmetic.

## Setup

```bash
python3.11 -m venv .venv
.venv/bin/python -m pip install soundfile==0.12.1 numpy==1.26.4
```

The repository root contains an `audiopipe -> src/audiopipe` symlink so
`python -m audiopipe.cli` works from a checkout without installing the project.
`pip install -e .` also works if you prefer a normal install.

## Demo

```bash
.venv/bin/python scripts/make_fixtures.py --out .tmp/input
.venv/bin/python -m audiopipe.cli batch \
  --input .tmp/input --output .tmp/output \
  --rate 48000 --channels preserve \
  --target-lufs -18 --max-peak-dbfs -1 \
  --jobs 2 --report .tmp/report.json
.venv/bin/python -m unittest discover -s tests
```

`scripts/make_fixtures.py --with-invalid` additionally writes a sibling
`<out>-invalid` tree (truncated payload, non-WAV bytes, IEEE-float WAV) for
exercising the failure path.

## Exit codes

| code | meaning |
| ---- | ------- |
| 0 | batch finished, every file succeeded |
| 1 | at least one file failed (others still completed) |
| 2 | bad arguments, missing input, or invalid configuration |

## What the report records

Per file (`--report` summary entry, or one file per input with `--report-dir`):

- `params`: `sample_rate`, `sample_rate_in`, `channels`, `channels_in`,
  `channel_mode`, `frames_in`, `frames_out`, `frames_out_expected`,
  `ratio_up`/`ratio_down`, `block_size`, `boundary`, `kernel`
  (`zero_crossings`, `rolloff`, `cutoff`), `target_lufs`, `max_peak_dbfs`,
  `gain_db`, `gain_reason`, `limited_by_peak`, `duration_in_s`, `duration_out_s`
- `stats`: `rms_before`, `rms_after`, `lufs_before`, `lufs_after`,
  `peak_dbfs_before`, `peak_dbfs_after`, `clipped_samples_before`,
  `clipped_samples_after`, `silent_before`, `silent_after`, `loudness_defined`,
  plus `source_*` values measured on the delivered file before any DSP.
  Undefined loudness (silence) is `null`, never `NaN`.
- `reason`: `null` for a plain conversion, otherwise `silent`,
  `peak_limited`, `declicked`, or `empty_output`.

All paths are relative to the scanned input root; absolute machine paths never
appear in reports or terminal output.

## Processing rules

- **Resampling**: exact integer phase tracking (`pos = i * down / up`), so
  phase cannot drift over long files. The kernel is a Blackman-windowed sinc
  whose cutoff is `rolloff * min(1, rate_out / rate_in)`, i.e. pulled in
  automatically when downsampling. Edge samples follow an explicit mirror rule
  (`-1 -> 0`, `-2 -> 1`, `N -> N-1`, ...), recorded as `boundary`.
- **Frame count**: `round_half_up(frames_in * rate_out / rate_in)`; the final
  block is zero-padded for processing then trimmed back to that count.
- **Degenerate inputs**: empty files produce empty output; single-frame and
  sub-kernel-length files are defined by the mirror rule.
- **Channels**: `--channels preserve` keeps interleaved order.
  `--channels mono` averages all channels to one. `--channels N` (or
  `exact:N`) rejects files whose channel count differs, so channels are never
  dropped silently.
- **Loudness**: BS.1770-style K-weighting, 400 ms blocks with 75 % overlap,
  absolute gate at −70 LUFS and relative gate at −10 LU. Per-channel mean
  squares are summed, so dual-mono material reads 3 dB above a single channel.
- **Gain**: one linear gain for the whole file. If the target LUFS would push
  the peak above `--max-peak-dbfs`, the gain is reduced and the file is marked
  `peak_limited`. Silence keeps gain 0 dB and reports `lufs: null`.
- **Output**: PCM_24 WAV, written to a sibling temp file, fsynced and renamed
  atomically; a rerun never leaves a half-written product behind.
- **Limits**: sample rate ≤ 384 kHz, channels ≤ 32, frames ≤ 192 000 * 3600,
  file size ≤ `--max-file-mb` (default 1024). Exceeding them fails that file
  with a specific reason (`sample_rate_limit`, `channel_limit`,
  `frame_limit`, `file_size_limit`).

## Robustness

- Files are validated against their RIFF chunk sizes, so a truncated payload
  is reported as `corrupt` instead of silently reading fewer frames.
- Only local PCM WAV is accepted; IEEE float, RIFX and non-WAV data are
  rejected with `unsupported_encoding` / `corrupt`.
- Each file is processed in isolation, so one failure does not abort the batch;
  the batch summary lists every failure with its reason.
- The output directory is pruned from discovery, so repeated runs into
  `--input/...` never re-ingest their own products.

## Layout

```
src/audiopipe/cli.py       argument parsing, exit codes
src/audiopipe/batch.py     discovery, parallelism, output naming, roll-up
src/audiopipe/pipeline.py  per-file orchestration and report record
src/audiopipe/resample.py  polyphase windowed-sinc resampler
src/audiopipe/layout.py    frame blocking, channel mapping, tail trimming
src/audiopipe/loudness.py  RMS, K-weighted LUFS, peak and gain decisions
src/audiopipe/report.py    JSON and terminal rendering
src/audiopipe/wavio.py     PCM WAV validation and atomic writes
src/audiopipe/config.py    validated configuration and input limits
tests/                     numerical boundary and end-to-end CLI tests
scripts/make_fixtures.py   deterministic generated fixtures
```
