"""Offline timeline compositing toolkit.

The package composes a declarative timeline of layered PNG assets into
in-memory RGBA frames, emits per-frame SHA-256 summaries, and reports
audio/video alignment drift. It never writes frame images to disk.
"""

__version__ = "0.1.0"
