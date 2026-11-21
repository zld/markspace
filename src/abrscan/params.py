"""Argument parsing and validation for scan parameters.

All validation failures raise :class:`ParamError`, which the CLI maps to
exit code 2.  Keeping this module dependency-free makes the rules easy to
unit test without touching the database or the codec.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Iterable, Sequence

# Scan limits keep a single run small and bounded.
MAX_QUANT_STEPS = 12
MAX_BLOCK_SIZES = 4
MAX_SCAN_COMBOS = 24
MAX_TARGETS = 16
MAX_FRAMES = 240
MAX_WIDTH = 320
MAX_HEIGHT = 240
MIN_DIMENSION = 8

DEFAULT_QUANT_STEPS: tuple[int, ...] = (8, 32, 64, 128, 200, 255)
DEFAULT_BLOCK_SIZES: tuple[int, ...] = (8, 16)

VALID_BLOCK_SIZES = (4, 8, 16)
FIXTURE_NAMES = ("moving-texture", "pan-texture", "static-texture")


class ParamError(ValueError):
    """Raised when command line values are malformed or out of range."""


@dataclass(frozen=True)
class ScanSpec:
    """A validated, deduplicated scan configuration."""

    quant_steps: tuple[int, ...]
    block_sizes: tuple[int, ...]
    targets_kbps: tuple[int, ...]
    duplicate_targets: tuple[int, ...]

    @property
    def combos(self) -> tuple[tuple[int, int], ...]:
        """Cartesian product of quantisation steps and block sizes."""
        return tuple(
            (quant, block)
            for quant in self.quant_steps
            for block in self.block_sizes
        )


def _parse_int_list(raw: str | None, label: str) -> list[int]:
    if raw is None:
        raise ParamError(f"{label} is required")
    text = raw.strip()
    if not text:
        raise ParamError(f"{label} must not be empty")
    values: list[int] = []
    for part in text.split(","):
        part = part.strip()
        if not part:
            raise ParamError(f"{label} contains an empty value")
        try:
            values.append(int(part))
        except ValueError as exc:
            raise ParamError(f"{label} value {part!r} is not an integer") from exc
    return values


def _check_unique(values: Sequence[int], label: str) -> tuple[int, ...]:
    seen: set[int] = set()
    result: list[int] = []
    for value in values:
        if value not in seen:
            seen.add(value)
            result.append(value)
    return tuple(result)


def parse_targets(raw: str | None) -> tuple[tuple[int, ...], tuple[int, ...]]:
    """Return (unique targets in input order, duplicate targets)."""
    values = _parse_int_list(raw, "--targets-kbps")
    if len(values) > MAX_TARGETS:
        raise ParamError(f"at most {MAX_TARGETS} targets are allowed")
    unique: list[int] = []
    duplicates: list[int] = []
    seen: set[int] = set()
    for value in values:
        if value <= 0 or value > 200_000:
            raise ParamError(f"target {value} is out of range (1..200000 kbps)")
        if value in seen:
            if value not in duplicates:
                duplicates.append(value)
            continue
        seen.add(value)
        unique.append(value)
    if not unique:
        raise ParamError("--targets-kbps must contain at least one target")
    return tuple(unique), tuple(duplicates)


def parse_scan_spec(
    targets_raw: str | None,
    quant_raw: str | None = None,
    block_raw: str | None = None,
) -> ScanSpec:
    """Validate scan ranges and reject empty or oversized combinations."""
    quant_values = (
        _parse_int_list(quant_raw, "--quant-steps")
        if quant_raw is not None
        else list(DEFAULT_QUANT_STEPS)
    )
    block_values = (
        _parse_int_list(block_raw, "--block-sizes")
        if block_raw is not None
        else list(DEFAULT_BLOCK_SIZES)
    )

    if not quant_values:
        raise ParamError("--quant-steps must not be empty")
    if not block_values:
        raise ParamError("--block-sizes must not be empty")
    if len(quant_values) > MAX_QUANT_STEPS:
        raise ParamError(f"at most {MAX_QUANT_STEPS} quantisation steps are allowed")
    if len(block_values) > MAX_BLOCK_SIZES:
        raise ParamError(f"at most {MAX_BLOCK_SIZES} block sizes are allowed")

    for value in quant_values:
        if value < 1 or value > 255:
            raise ParamError(f"quantisation step {value} is out of range (1..255)")
    for value in block_values:
        if value not in VALID_BLOCK_SIZES:
            raise ParamError(
                f"block size {value} is not supported (choose from {VALID_BLOCK_SIZES})"
            )

    quant_unique = _check_unique(quant_values, "--quant-steps")
    block_unique = _check_unique(block_values, "--block-sizes")
    total = len(quant_unique) * len(block_unique)
    if total == 0:
        raise ParamError("scan range is empty")
    if total > MAX_SCAN_COMBOS:
        raise ParamError(
            f"scan would run {total} combinations; limit is {MAX_SCAN_COMBOS}"
        )

    targets, duplicates = parse_targets(targets_raw)
    return ScanSpec(
        quant_steps=quant_unique,
        block_sizes=block_unique,
        targets_kbps=targets,
        duplicate_targets=duplicates,
    )


def validate_fixture_geometry(
    name: str, width: int, height: int, frames: int, block_sizes: Iterable[int]
) -> None:
    """Reject fixtures that cannot host the requested block grid."""
    if name not in FIXTURE_NAMES:
        raise ParamError(f"unknown fixture {name!r}; choose from {FIXTURE_NAMES}")
    if not (MIN_DIMENSION <= width <= MAX_WIDTH):
        raise ParamError(f"width must be in {MIN_DIMENSION}..{MAX_WIDTH}")
    if not (MIN_DIMENSION <= height <= MAX_HEIGHT):
        raise ParamError(f"height must be in {MIN_DIMENSION}..{MAX_HEIGHT}")
    if not (1 <= frames <= MAX_FRAMES):
        raise ParamError(f"frame count must be in 1..{MAX_FRAMES}")
    for block in block_sizes:
        if width % block or height % block:
            raise ParamError(
                f"{width}x{height} is not divisible by block size {block}"
            )


def parse_workers(raw: str | None) -> int:
    if raw is None:
        return 1
    try:
        workers = int(raw)
    except ValueError as exc:
        raise ParamError(f"--workers value {raw!r} is not an integer") from exc
    if workers < 1 or workers > 16:
        raise ParamError("--workers must be in 1..16")
    return workers


def parse_stop_after(raw: str | None) -> int | None:
    if raw is None:
        return None
    try:
        value = int(raw)
    except ValueError as exc:
        raise ParamError(f"--stop-after value {raw!r} is not an integer") from exc
    if value < 1:
        raise ParamError("--stop-after must be a positive integer")
    return value
