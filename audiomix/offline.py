"""Offline driver: renders the whole timeline in a single block."""

from __future__ import annotations

import numpy as np

from .graph import MixEngine


def render_offline(engine: MixEngine) -> np.ndarray:
    """Render the full mix in one pass; returns the pre-limit master."""
    engine.reset()
    return engine.process_block(0, engine.total_frames)
