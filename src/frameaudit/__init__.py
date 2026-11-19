"""frameaudit: local video frame-order and timestamp diagnostics.

The package is intentionally local-only: it reads video files through PyAV,
reorders B-frame decode output into presentation order, repairs faulty
timestamps and emits matching terminal and JSON reports.
"""

from __future__ import annotations

__version__ = "0.1.0"

__all__ = ["__version__"]
