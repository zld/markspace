"""Import shim so ``python -m frame_audit.cli`` works from a raw checkout.

The real package source lives in ``src/frame_audit``.  This file keeps the
working tree install-free while still allowing the module to be imported from
the project root (``tools/``, ``tests/`` and the acceptance commands all rely on
that).  Installed copies use ``src/frame_audit`` directly.
"""

from pathlib import Path as _Path

_SRC = _Path(__file__).resolve().parent.parent / "src" / "frame_audit"
__path__ = [str(_SRC)]
_INIT = _SRC / "__init__.py"
exec(compile(_INIT.read_text(encoding="utf-8"), str(_INIT), "exec"))
