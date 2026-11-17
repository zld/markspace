"""audiocut: local PCM WAV silence detection and dialogue slicing.

The package is fully local: it only depends on the Python standard library
(``wave``, ``sqlite3``, ``argparse`` ...) and NumPy.  No third-party silence
detector is used; the detection algorithm lives in :mod:`audiocut.detector`.
"""

__version__ = "0.1.0"

__all__ = ["__version__"]
