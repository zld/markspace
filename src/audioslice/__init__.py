"""audioslice: local audio segment / silence detection and packaging tool.

The package layout lives under ``src/audioslice``.  A tiny path-extension shim
exists at the repository root (``audioslice/__init__.py``) so the acceptance
commands can run ``python -m audioslice.cli`` straight from a checkout without
an install step; both locations resolve to this same package.
"""

from .model import AnalysisParams, AudioData, DetectionResult, Region

__all__ = ["AnalysisParams", "AudioData", "DetectionResult", "Region"]
__version__ = "0.1.0"
