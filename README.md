# audioprep

Local, reproducible batch audio preparation for game engine material. It turns
heterogeneous delivery audio (mixed sample rates, channel layouts and loudness)
into engine-ready 24-bit PCM WAV files plus an offline, re-computable JSON
report. Everything runs on this machine: no network, no GUI, no device access.

Resampling and loudness measurement are implemented from scratch in NumPy. The
only third-party packages are `soundfile` (WAV/FLAC I/O), `numpy` and `pytest`.

## Setup

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install --upgrade pip
.venv/bin/python -m pip install soundfile==0.14.0 numpy==2.4.3 pytest==9.2.0
```

## Usage

```sh
# 1. Generate three deterministic WAV samples (no external media).
.venv/bin/python -m audioprep fixtures --out .work/in

# 2. Batch convert: resample to 48 kHz stereo, normalize to -18 LUFS,
#    cap sample peaks at -1 dBFS, stream in 4096-frame blocks, 2 workers.
.venv/bin/python -m audioprep batch \
  --input .work/in --output .work/out --report .work/report.json \
  --sample-rate 48000 --channels stereo --target-lufs -18 \
  --peak-ceiling -1 --block-frames 4096 --workers 2

# 3. Independently re-read the rendered WAVs and recompute every statistic.
.venv/bin/python -m audioprep verify --report .work/report.json --output .work/out

# Tests
.venv/bin/python -m pytest -q
```

## What the pipeline does

1. **Scan** — recursively collect `.wav`/`.flac` in relative-path order.
   Symlinks escaping the input root, duplicate output targets and unsupported
   extensions are rejected as configuration errors. Corrupt files, zero-frame
   inputs and non-finite samples are recorded per file while the batch continues.
2. **Channel mapping** — fixed, frame-accurate mono/stereo rules (`keep`,
   `mono` downmix `(L+R)/2`, `stereo` mono duplication).
3. **Resample** — from-scratch polyphase windowed-sinc kernel supporting
   8–192 kHz, with an anti-alias cutoff derived from the rate ratio. Coefficient
   plans are built once per rate pair and shared read-only across threads; each
   file keeps independent phase state. Tail zero-padding participates in
   convolution but is trimmed to `ceil(N * out/in)` and never counted in
   duration or loudness.
4. **Loudness** — BS.1770-style K-weighting (biquad-derived impulse response
   applied by FFT overlap-add), 400 ms / 75%-overlap windows, absolute (-70 LKFS)
   and relative gating. Material shorter than one window uses a whole-length
   fallback (flagged); silence reports `null` rather than infinite dB.
5. **Normalize** — gain to the target LUFS, then constrained by the sample-peak
   ceiling. RMS, LUFS and full-scale sample counts are recorded before and after.
6. **Write** — stream to a temporary sibling WAV (`PCM_24`) and atomically
   replace the target only on success; failures remove the temp file.
7. **Report / verify** — sorted JSON with input/output SHA-256 hashes, sample
   rates, channels, parameters and all statistics. `verify` recomputes the
   output-side fields independently and exits non-zero on any mismatch.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | success / all files verified |
| 1 | one or more files failed (batch still completed) |
| 2 | usage or configuration error |
| 3 | verify found a report mismatch |

## Memory model

Processing is frame-blocked. Retained state (resampler buffer, K-weight carry,
the 400 ms loudness window) is sized by `--block-frames` and the filter kernel,
not by clip duration, so long files do not grow peak memory.
