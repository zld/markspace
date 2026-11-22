"""Shared helpers for the JSON exchange format readers/writers."""

from __future__ import annotations

import json
from fractions import Fraction
from pathlib import Path
from typing import Any

from .model import ResolutionError


def load_json(path: str | Path) -> Any:
    """Load JSON from ``path``; parse failures become :class:`ResolutionError`."""
    try:
        with Path(path).open("r", encoding="utf-8") as handle:
            return json.load(handle)
    except FileNotFoundError as exc:
        raise ResolutionError(f"input file not found: {path}") from exc
    except json.JSONDecodeError as exc:
        raise ResolutionError(f"invalid JSON in {path}: {exc}") from exc


def dump_json(payload: Any, path: str | Path) -> None:
    with Path(path).open("w", encoding="utf-8") as handle:
        json.dump(payload, handle, indent=2, sort_keys=True)
        handle.write("\n")


def require_object(data: Any) -> dict[str, Any]:
    if not isinstance(data, dict):
        raise ResolutionError(
            f"exchange document must be a JSON object, got {type(data).__name__}"
        )
    return data


def require_field(obj: dict[str, Any], field: str, context: str) -> Any:
    if field not in obj or obj[field] is None:
        raise ResolutionError(f"missing required field {field!r} in {context}")
    return obj[field]


def require_int(value: Any, field: str) -> int:
    # bool is a subclass of int but is never a valid frame rate/priority.
    if not isinstance(value, int) or isinstance(value, bool):
        raise ResolutionError(f"field {field!r} must be an integer, got {value!r}")
    return value


def as_fraction(value: Any, field: str) -> Fraction:
    """Promote a JSON time value to an exact :class:`Fraction`.

    Floats are converted via their shortest decimal repr instead of their
    binary expansion so decimal inputs round as a human editor expects; no
    floating-point arithmetic is ever accumulated.
    """
    if isinstance(value, bool):
        raise ResolutionError(f"field {field!r} must be a number, got boolean")
    if isinstance(value, int):
        return Fraction(value)
    if isinstance(value, float):
        return Fraction(repr(value))
    if isinstance(value, str):
        try:
            return Fraction(value)
        except ValueError as exc:
            raise ResolutionError(
                f"field {field!r} must be numeric, got {value!r}"
            ) from exc
    raise ResolutionError(f"field {field!r} must be numeric, got {value!r}")


def check_version(data: dict[str, Any], expected: int) -> None:
    version = require_field(data, "version", "exchange document")
    if not isinstance(version, int) or isinstance(version, bool):
        raise ResolutionError(f"unsupported format version: {version!r}")
    if version != expected:
        raise ResolutionError(
            f"unsupported format version {version}; this reader supports v{expected}"
        )
