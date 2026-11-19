"""Offline WAV resampling and loudness-normalisation pipeline.

The package is intentionally split into small, independent modules so the
batch runner can compose them:

* :mod:`audiopipe.io` -- PCM WAV reading/writing (SoundFile)
* :mod:`audiopipe.resample` -- self-contained polyphase FIR resampler
* :mod:`audiopipe.loudness` -- RMS / K-weighted loudness / gain decisions
* :mod:`audiopipe.report` -- deterministic, strictly-finite JSON reports
* :mod:`audiopipe.batch` -- directory scan, scheduling and aggregation
* :mod:`audiopipe.cli` -- argument parsing and exit-code mapping
"""

from __future__ import annotations

__all__ = ["__version__"]

__version__ = "0.1.0"
