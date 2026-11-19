"""Repository-root path shim.

The real package lives at ``src/audioslice`` (see ``pyproject.toml``).  The
acceptance workflow runs ``python -m audioslice.cli`` directly from a plain
checkout without an editable install, so this thin package redirects the
import machinery to the sources under ``src/`` by rewriting ``__path__``.
When the package is installed normally, ``src/audioslice`` takes precedence and
this directory is never imported.
"""

from pathlib import Path
import sys

_SRC = Path(__file__).resolve().parent.parent / "src"
if str(_SRC) not in sys.path:
    sys.path.insert(0, str(_SRC))

# Route every `audioslice.<submodule>` lookup into the real source tree.
__path__ = [str(_SRC / "audioslice")]

from .model import AnalysisParams, AudioData, DetectionResult, Region  # noqa: E402

__all__ = ["AnalysisParams", "AudioData", "DetectionResult", "Region"]
__version__ = "0.1.0"
