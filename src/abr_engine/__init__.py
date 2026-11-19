"""Offline adaptive bitrate (ABR) decision engine."""

from .estimator import Estimate, ThroughputEstimator
from .models import (
    NetworkSample,
    PlaybackState,
    Segment,
    SegmentRecord,
    ValidationError,
    require_finite,
    validate_ladder,
)
from .policy import AbrPolicy, Decision, PolicyConfig
from .report import format_summary, summarize, to_payload
from .simulator import SimulationError, SimulationResult, simulate
from .trace import generate_trace, load_trace, save_trace

__all__ = [
    "AbrPolicy",
    "Decision",
    "Estimate",
    "NetworkSample",
    "PlaybackState",
    "PolicyConfig",
    "Segment",
    "SegmentRecord",
    "SimulationError",
    "SimulationResult",
    "ThroughputEstimator",
    "ValidationError",
    "format_summary",
    "generate_trace",
    "load_trace",
    "require_finite",
    "save_trace",
    "simulate",
    "summarize",
    "to_payload",
    "validate_ladder",
]

__version__ = "0.1.0"
