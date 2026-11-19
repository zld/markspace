"""Source-layout import shim for uninstalled local runs.

The real package lives in ``src/mixforge``.  Running ``python -m mixforge.cli``
from a fresh checkout would otherwise fail before ``pip install -e .``.  This
package points ``__path__`` at the src layout without duplicating code; the
version and all submodules come from there.
"""

from pathlib import Path

_SRC = Path(__file__).resolve().parent.parent / "src" / "mixforge"
if _SRC.is_dir():
    __path__ = [str(_SRC)]

from ._version import __version__  # noqa: E402  (resolved via __path__)

__all__ = ["__version__"]
