"""Cutline: deterministic editorial timeline model and offline render sampling."""

from __future__ import annotations

__version__ = "0.1.0"

from .errors import (  # noqa: E402,F401
    BridgeError,
    ConflictError,
    CutlineError,
    ExchangeError,
    ModelError,
    RenderError,
    ResolutionError,
    TimebaseError,
)
from .timebase import FrameRate, FrameSpan  # noqa: E402,F401

__all__ = [
    "__version__",
    "BridgeError",
    "ConflictError",
    "CutlineError",
    "ExchangeError",
    "FrameRate",
    "FrameSpan",
    "ModelError",
    "RenderError",
    "ResolutionError",
    "TimebaseError",
]
