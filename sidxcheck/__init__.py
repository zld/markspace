"""sidxcheck: offline verification of ISO BMFF sidx indexes and stbl sample tables.

The real package lives under ``src/sidxcheck``.  This directory only carries a
thin loader shim so that ``python -m sidxcheck.cli`` works straight from a
checkout without an installation step; ``pyproject.toml`` points packaging at
the implementation under ``src/``.
"""

from __future__ import annotations

import sys
from pathlib import Path

_SRC = Path(__file__).resolve().parent.parent / "src"
if _SRC.is_dir() and str(_SRC) not in sys.path:
    sys.path.insert(0, str(_SRC))

from sidxcheck import *  # noqa: F401,F403,E402
from sidxcheck import __version__  # noqa: F401,E402
