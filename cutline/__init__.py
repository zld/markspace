"""Repository-root bootstrap for the ``src/`` layout.

Acceptance runs ``python -m cutline.cli`` straight from a fresh checkout with
only the third-party dependencies installed, so the package has to be
importable without ``pip install``.  This module makes the ``cutline`` package
resolve to the real sources in ``src/cutline`` and executes their ``__init__``
so both import paths expose exactly the same API.

Installed distributions (``pip install -e .``) use ``src/cutline`` directly;
this shim is only what the working-tree import picks up.
"""

from __future__ import annotations

from pathlib import Path

_SOURCE = Path(__file__).resolve().parent.parent / "src" / "cutline"
__path__ = [str(_SOURCE)]

_init = _SOURCE / "__init__.py"
exec(compile(_init.read_text(encoding="utf-8"), str(_init), "exec"))  # noqa: S102
