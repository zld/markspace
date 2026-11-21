"""audioslice: local PCM WAV silence detection and segmentation.

The package is intentionally layered so that :mod:`audioslice.detector`
depends neither on the CLI nor on the SQLite store.  Both the command line
entry point and :mod:`audioslice.batch` reuse the same detector code.
"""

__version__ = "0.1.0"
