# audiorepro

Offline regression-baseline builder for game-audio QA. It ingests WAV,
FLAC and AIFF recordings from different developer machines, resamples
them to one rate, normalizes loudness under a peak ceiling, and writes a
versioned JSON report that can be independently re-verified.

Everything runs locally in the terminal. There is no GUI, no device
access, and no network dependency.

## Requirements

- macOS 13+ (Apple Silicon or Intel), Python 3.11
- NumPy 1.26.4, SoundFile 0.12.1 (installed into the project `.venv`)

```sh
brew install python@3.11
python3.11 -m venv .venv
.venv/bin/python -m pip install numpy==1.26.4 soundfile==0.12.1
```

## Usage

```sh
# Compile and test
PYTHONPATH=src .venv/bin/python -m compileall -q src tests
PYTHONPATH=src .venv/bin/python -m unittest discover -s tests -v

# Generate six deterministic fixtures (mono, stereo, 5.1, short, silent, hot)
PYTHONPATH=src .venv/bin/python -m audiorepro fixture --out .work/input --seed 17

# Resample, normalize and report
PYTHONPATH=src .venv/bin/python -m audiorepro process \
  --input .work/input --output .work/output --report .work/report.json \
  --rate 48000 --target-lufs -23 --peak-dbfs -1 --jobs 2 --resume

# Independently re-read sources and products and compare the report
PYTHONPATH=src .venv/bin/python -m audiorepro verify \
  --input .work/input --output .work/output --report .work/report.json \
  --tolerance 0.15
```

Useful extra flags: `--chunk-frames` caps the per-block working set for
`process`; `--json-out` writes the verification result to a file.

## Exit codes

| Code | Meaning |
| ---- | ------- |
| 0 | all files processed, or verification within tolerance |
| 2 | invalid command line or parameters |
| 3 | at least one file failed processing, or verification found differences |
| 4 | report/output could not be written, or report could not be read |

## What the pipeline guarantees

- **Discovery** is recursive and sorted. Symlinks are rejected, duplicate
  output names (for example `take.wav` and `take.aiff` in one directory)
  are reported as conflicts, and a corrupt file is recorded without
  stopping the rest of the batch.
- **Resampling** uses a polyphase Blackman-windowed sinc kernel with the
  cutoff at the lower Nyquist frequency, so downsampling is
  anti-aliased. Coefficient tables are tabulated per fractional phase and
  cached per rate pair for reuse across the batch. Output length is
  rounded up and convolution edges are zero-padded.
- **Supported audio**: 1, 2 or 6 channels with channel order preserved
  (WAV 5.1 order FL FR FC LFE BL BR; LFE is excluded from loudness), and
  rates from 8 kHz to 192 kHz. Anything else is an explicit per-file
  error.
- **Loudness** is ITU-R BS.1770-4: K-weighting derived at each file's
  actual rate, 400 ms blocks with 100 ms hops, absolute (-70 LUFS) and
  relative (-10 LU) gates. Clips shorter than 400 ms or fully silent
  yield `lufs = null`.
- **Normalization** applies the gain needed to reach `--target-lufs`
  unless that would exceed `--peak-dbfs`; in that case gain is reduced to
  the ceiling. Clipped source samples and clipped output samples are
  counted, and non-finite samples abort that file rather than being
  written.
- **Resume** keys on the algorithm parameter hash plus SHA-256 of both
  the source and the existing product; changed inputs or settings force
  reprocessing.
- **Atomicity**: products are written to a temporary sibling and moved
  into place with `os.replace`; interrupted runs leave no half-written
  WAV. Input files are opened read-only and never overwritten (a
  same-path run is refused).

## Report layout

```jsonc
{
  "schema": "audiorepro.report/1",
  "tool_version": "1.0.0",
  "params": { "sample_rate": 48000, "target_lufs": -23, "resampler": { ... },
              "loudness": { "standard": "ITU-R BS.1770-4", ... } },
  "params_hash": "...",
  "summary": { "files": 6, "failed": 0, "resumed": 0, "sample_rate": 48000,
               "channels": { "1": 3, "2": 2, "6": 1 } },
  "files": [
    { "relative_path": "mono/mono_tone.wav",
      "rms_before_dbfs": -23.04, "lufs_before": -23.01,
      "rms_after_dbfs": -23.03, "lufs_after": -23.0,
      "clip_samples_before": 0, "clip_samples_after": 0,
      "gain_db": 0.01, "source_sha256": "...", "output_sha256": "...",
      "algorithm": { "input_k_weighting": { ... }, "resampler": { ... } } }
  ],
  "errors": [ { "path": "...", "stage": "...", "reason": "..." } ]
}
```

Resume state lives in `<output>/.audiorepro-state.json`. It is an
implementation detail and is ignored by `verify`.

## Notes

- The report intentionally records measurement parameters per input and
  output rate so a later `verify` (or a human) can reproduce the numbers.
- `verify` recomputes everything from the files themselves; it does not
  trust the cached values in the report.
