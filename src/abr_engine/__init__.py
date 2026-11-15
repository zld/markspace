"""Local ABR (adaptive bitrate) decision engine.

Simulates bitrate selection over synthetic network traces: an estimator
tracks throughput, a policy picks ladder rungs with hysteresis and buffer
protection, and a simulator advances download/playback state per segment.
"""

from abr_engine.models import (
    BufferState,
    ConfigError,
    DecisionRecord,
    Ladder,
    NetworkSample,
)

__version__ = "0.1.0"

__all__ = [
    "BufferState",
    "ConfigError",
    "DecisionRecord",
    "Ladder",
    "NetworkSample",
    "__version__",
]
