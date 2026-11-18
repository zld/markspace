"""Root-level shim package.

The acceptance harness runs ``python -m trackcheck.cli`` from the project
root without installing the distribution.  This package locates the real
implementation under ``src/trackcheck`` and swaps it into ``sys.modules``
under the canonical name, so ``import trackcheck`` and every submodule
(``trackcheck.cli``, ``trackcheck.probe`` ...) resolve to the src/ code —
the same modules an editable install would expose.
"""
from __future__ import annotations

import importlib.util
import sys
from pathlib import Path

_HERE = Path(__file__).resolve().parent
_REAL_INIT = _HERE.parent / "src" / "trackcheck" / "__init__.py"

_spec = importlib.util.spec_from_file_location(
    "trackcheck", _REAL_INIT, submodule_search_locations=[
        str(_REAL_INIT.parent),
    ],
)
assert _spec is not None and _spec.loader is not None
_module = importlib.util.module_from_spec(_spec)
sys.modules["trackcheck"] = _module
_spec.loader.exec_module(_module)
