"""Source-layout import shim for uninstalled local runs.

The real package lives in ``src/mixforge``.  Running ``python -m mixforge.cli``
from a fresh checkout would otherwise fail before ``pip install -e .``.  This
package exposes the src directory through ``__path__`` without duplicating code.
"""

from pathlib import Path

_SRC = Path(__file__).resolve().parent.parent / "src" / "mixforge"
if _SRC.is_dir():
    __path__ = [str(_SRC)]
