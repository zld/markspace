"""Shared helpers for the exchange formats."""

from __future__ import annotations

import json
from fractions import Fraction
from pathlib import Path
from typing import Any, Union

MAX_INPUT_BYTES = 10 * 1024 * 1024  # 10 MB


class FormatError(ValueError):
    """Raised when an exchange document is malformed or unrepresentable."""


class DocumentIOError(OSError):
    """Raised when an input document cannot be read from disk."""


def read_document(path: Union[str, Path]) -> dict[str, Any]:
    p = Path(path)
    try:
        size = p.stat().st_size
    except OSError as exc:
        raise DocumentIOError(f"cannot stat input {p}: {exc}") from exc
    if size > MAX_INPUT_BYTES:
        raise FormatError(
            f"input {p} is {size} bytes, exceeds {MAX_INPUT_BYTES} byte limit"
        )
    try:
        text = p.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        raise DocumentIOError(f"cannot read input {p}: {exc}") from exc
    try:
        doc = json.loads(text)
    except json.JSONDecodeError as exc:
        raise FormatError(f"input {p}: invalid JSON at line {exc.lineno}: {exc.msg}") from exc
    if not isinstance(doc, dict):
        raise FormatError(f"input {p}: top-level JSON value must be an object")
    return doc


def write_document(path: Union[str, Path], doc: dict[str, Any]) -> None:
    p = Path(path)
    text = json.dumps(doc, indent=2, sort_keys=False) + "\n"
    p.write_text(text, encoding="utf-8")


def fraction_to_json(value: Fraction) -> Union[int, list[int]]:
    if value.denominator == 1:
        return value.numerator
    return [value.numerator, value.denominator]


def fraction_from_json(value: Any, context: str) -> Fraction:
    if isinstance(value, bool):
        raise FormatError(f"{context}: boolean is not a rational number")
    if isinstance(value, int):
        return Fraction(value)
    if isinstance(value, (list, tuple)) and len(value) == 2:
        num, den = value
        if isinstance(num, bool) or isinstance(den, bool):
            raise FormatError(f"{context}: boolean is not a rational number")
        if isinstance(num, int) and isinstance(den, int):
            if den == 0:
                raise FormatError(f"{context}: zero denominator")
            return Fraction(num, den)
    raise FormatError(f"{context}: expected int or [num, den], got {value!r}")


def require(doc: dict[str, Any], key: str, context: str) -> Any:
    if key not in doc:
        raise FormatError(f"{context}: missing required key {key!r}")
    return doc[key]
