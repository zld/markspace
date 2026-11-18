"""Repository-local path shim.

The acceptance harness runs ``python -m trackcheck.cli`` from the project
root without installing the package.  Python imports this file at startup
(see ``sys.path`` handling of the script directory) and puts ``src`` on
``sys.path`` so the package resolves both for the CLI and for pytest.
"""
from __future__ import annotations

import sys
from pathlib import Path

_SRC = Path(__file__).resolve().parent / "src"
if _SRC.is_dir() and str(_SRC) not in sys.path:
    sys.path.insert(0, str(_SRC))
