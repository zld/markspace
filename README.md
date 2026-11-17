# audiocut

Local PCM WAV silence detection and dialogue slicing toolkit.

- Pure-local: standard library + NumPy only, no third-party silence detector.
- Streams PCM in 2-second blocks; never loads a whole recording into memory.
- Quantile noise-floor estimation, adaptive dual thresholds, duration gates.
- Sample-accurate boundaries, SQLite provenance, atomic WAV slice export.
- 8/16/24/32-bit PCM, mono or stereo; compressed/truncated WAV rejected.

## Quick start

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install numpy==2.2.6 setuptools==75.8.0
.venv/bin/python -m pip install --no-build-isolation -e .
.venv/bin/python -m unittest discover -s tests
.venv/bin/python -m audiocut demo --workdir /tmp/audiocut-demo
```

## Commands

- `analyze PATH...` — detect segments/silence, print sample boundaries.
- `export PATH... --outdir DIR` — write per-segment WAVs and a manifest.
- `demo --workdir DIR` — generate a synthetic fixture and run the pipeline.
