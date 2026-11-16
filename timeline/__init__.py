"""Reproducible cutscene timeline summary pipeline.

Modules:
- clock: rational frame clock
- model: timeline / layer / clip / keyframe data
- compositor: off-screen RGBA compositing
- audio_sync: synthetic audio cue clock and A/V alignment stats
- fixture: deterministic demo data generation
- exporter: per-frame hashing and atomic summary writer
- cli: terminal entry point
"""

__version__ = "0.1.0"
