"""Recursive discovery of WAV/FLAC inputs with extension/format consistency checks."""

from __future__ import annotations

from pathlib import Path

import soundfile as sf

from audiopack.models import (
    MAX_CHANNELS,
    MIN_CHANNELS,
    SUPPORTED_EXTENSIONS,
    AudioMeta,
    DiscoveryError,
)

# libsndfile container names we accept for each extension.
_EXPECTED_FORMAT = {".wav": "WAV", ".flac": "FLAC"}


def _is_within(child: Path, parent: Path) -> bool:
    try:
        child.relative_to(parent)
        return True
    except ValueError:
        return False


def validate_batch_paths(input_dir: Path, output_dir: Path) -> tuple[Path, Path]:
    """Resolve and cross-check input/output directories (batch-level rules)."""
    input_dir = input_dir.resolve()
    output_dir = output_dir.resolve()
    if not input_dir.is_dir():
        raise DiscoveryError(f"input directory does not exist: {input_dir}")
    if input_dir == output_dir:
        raise DiscoveryError("input and output directories must differ")
    if _is_within(output_dir, input_dir) or _is_within(input_dir, output_dir):
        raise DiscoveryError(
            f"input and output directories must not overlap: {input_dir} <-> {output_dir}"
        )
    return input_dir, output_dir


def discover_inputs(input_dir: Path) -> list[Path]:
    """Return supported audio files under ``input_dir``, sorted by relative path."""
    found: list[Path] = []
    for path in input_dir.rglob("*"):
        if path.is_file() and path.suffix.lower() in SUPPORTED_EXTENSIONS:
            found.append(path)
    return sorted(found, key=lambda p: p.relative_to(input_dir).as_posix())


def plan_outputs(
    input_dir: Path, output_dir: Path, inputs: list[Path]
) -> list[tuple[Path, Path, str]]:
    """Map each input to a unique, non-existing output path.

    Returns (input, output, relpath) triples. Raises DiscoveryError on duplicate
    target paths or targets that already exist on disk (never overwrite).
    """
    plan: list[tuple[Path, Path, str]] = []
    seen: dict[Path, str] = {}
    for src in inputs:
        rel = src.relative_to(input_dir)
        target = (output_dir / rel).with_suffix(".wav")
        key = target.resolve()
        if key in seen:
            raise DiscoveryError(
                f"duplicate output path {target}: from {seen[key]!r} and {rel.as_posix()!r}"
            )
        if target.exists():
            raise DiscoveryError(f"output file already exists: {target}")
        seen[key] = rel.as_posix()
        plan.append((src, target, rel.with_suffix(".wav").as_posix()))
    return plan


def probe_file(path: Path) -> AudioMeta:
    """Read container metadata and verify it matches the file extension."""
    ext = path.suffix.lower()
    try:
        info = sf.info(path)
    except Exception as exc:  # corrupt or unreadable container
        raise DiscoveryError(f"unreadable or corrupt file {path.name}: {exc}") from exc
    expected = _EXPECTED_FORMAT.get(ext)
    if expected is not None and info.format != expected:
        raise DiscoveryError(
            f"extension/format mismatch for {path.name}: "
            f"extension implies {expected}, container is {info.format}"
        )
    if not (MIN_CHANNELS <= info.channels <= MAX_CHANNELS):
        raise DiscoveryError(
            f"unsupported channel count {info.channels} in {path.name} "
            f"(allowed {MIN_CHANNELS}..{MAX_CHANNELS})"
        )
    return AudioMeta(
        path=path,
        sample_rate=info.samplerate,
        channels=info.channels,
        frames=info.frames,
        format=info.format,
        subtype=info.subtype,
    )
