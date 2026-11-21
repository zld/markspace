"""mkvmp4: a local, dependency-light Matroska to MP4 remux pipeline.

The package never delegates container parsing, timestamp rewriting or MP4
muxing to a third-party library.  NumPy is used only for integer time-base
conversion and batch timestamp validation.
"""

__version__ = "0.1.0"

__all__ = ["__version__"]
