"""Import shim: redirect ``audioslice`` imports to the real package in src/.

The project uses a src/ layout but the acceptance workflow runs
``python -m audioslice.cli`` and ``python -m unittest discover -s tests``
from the repository root without an editable install. The script directory
(repo root) is first on sys.path, so this thin package is imported first; it
loads the real package from src/ and swaps itself out of sys.modules.

An installed version of the package always wins because site-packages comes
after cwd only for in-tree execution; nothing here runs when the package is
already importable through a normal install.
"""

import importlib
import importlib.util
import sys
from pathlib import Path

_REAL_SRC = Path(__file__).resolve().parent.parent / "src"

if str(_REAL_SRC) not in sys.path:
    sys.path.insert(0, str(_REAL_SRC))

# Drop this shim so the real package (now discoverable via src/) takes over.
sys.modules.pop(__name__, None)
_real_spec = importlib.util.find_spec("audioslice")
if _real_spec is None:  # pragma: no cover - defensive; src/ is part of the repo
    raise ImportError("real audioslice package not found under src/")
_real_pkg = importlib.util.module_from_spec(_real_spec)
_real_spec.loader.exec_module(_real_pkg)
sys.modules["audioslice"] = _real_pkg
