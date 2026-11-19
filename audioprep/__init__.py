"""Repo-root shim so ``python -m audioprep.cli`` works from the project root
without installing the package. The real package lives in ``src/audioprep``
(see pyproject.toml); this shim only extends the package search path and
re-executes the real ``__init__`` so metadata such as VERSION is available.
"""
from pathlib import Path

_src = Path(__file__).resolve().parent.parent / "src" / "audioprep"
if _src.is_dir():
    __path__.append(str(_src))  # type: ignore[name-defined]
    _init = _src / "__init__.py"
    if _init.is_file():
        exec(compile(_init.read_text(encoding="utf-8"), str(_init), "exec"), globals())
