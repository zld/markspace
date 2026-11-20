"""Recursive input discovery and output path-conflict validation."""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path

from . import SUPPORTED_LAYOUTS

SUPPORTED_SUFFIXES = {".wav", ".flac"}


@dataclass(frozen=True)
class SourceFile:
    """One discovered audio source and its planned output location."""

    source_path: Path
    relative_path: Path
    output_path: Path
    channels: int
    sample_rate: int


class DiscoveryError(Exception):
    """Raised for path layout problems before any audio is processed."""


def _is_within(child: Path, parent: Path) -> bool:
    """Path-prefix test that works even when ``child`` does not exist."""
    try:
        child_abs = child.absolute()
        parent_abs = parent.absolute()
        child_abs.relative_to(parent_abs)
        return True
    except ValueError:
        return False


def _probe_channels(path: Path, sf) -> int:
    """Read only the file header to obtain the channel count."""
    info = sf.info(str(path))
    return int(info.channels)


def discover_sources(input_dir: Path, output_dir: Path,
                     ) -> tuple[list[SourceFile], list[dict]]:
    """Recursively enumerate WAV/FLAC files and plan WAV outputs.

    Returns ``(sources, pre_errors)``.  ``pre_errors`` records files that
    cannot be processed (unsupported channel layout, unreadable header)
    so the batch can report them without touching the valid files.
    Structural problems (nested directories, output collisions) raise
    :class:`DiscoveryError` instead, since they make the whole run
    ambiguous.
    """
    input_dir = Path(input_dir)
    output_dir = Path(output_dir)
    if not input_dir.is_dir():
        raise DiscoveryError(f"input directory not found: {input_dir}")
    in_resolved = input_dir.resolve()
    out_resolved = output_dir.resolve()
    if in_resolved == out_resolved:
        raise DiscoveryError(
            "input and output directories must differ: "
            f"{input_dir} == {output_dir}")
    if _is_within(out_resolved, in_resolved):
        raise DiscoveryError(
            "output directory must not be nested inside input "
            f"directory: {output_dir} is under {input_dir}")
    if _is_within(in_resolved, out_resolved):
        raise DiscoveryError(
            "input directory must not be nested inside output "
            f"directory: {input_dir} is under {output_dir}")

    import soundfile as sf

    sources: list[SourceFile] = []
    pre_errors: list[dict] = []
    seen_outputs: dict[Path, Path] = {}
    for dirpath, dirnames, filenames in os.walk(in_resolved):
        dirnames.sort()
        for name in sorted(filenames):
            path = Path(dirpath) / name
            suffix = path.suffix.lower()
            if suffix not in SUPPORTED_SUFFIXES:
                if suffix in {".mp3", ".aac", ".ogg", ".m4a", ".aiff"}:
                    pre_errors.append({
                        "source": str(path.relative_to(in_resolved)),
                        "status": "error",
                        "error": f"unsupported format: {suffix}",
                    })
                continue
            relative = path.relative_to(in_resolved)
            output_path = output_dir / relative.with_suffix(".wav")
            if output_path in seen_outputs:
                raise DiscoveryError(
                    "output path collision: "
                    f"{seen_outputs[output_path]} and {path} both map to "
                    f"{output_path}")
            seen_outputs[output_path] = path
            try:
                channels = _probe_channels(path, sf)
                info = sf.info(str(path))
                sample_rate = int(info.samplerate)
            except Exception as exc:  # corrupt/unreadable header
                pre_errors.append({
                    "source": str(relative),
                    "status": "error",
                    "error": f"cannot read audio header: {exc}",
                })
                continue
            if channels not in SUPPORTED_LAYOUTS:
                pre_errors.append({
                    "source": str(relative),
                    "status": "error",
                    "error": (f"unsupported channel layout: {channels} "
                              "channels (only 1, 2, 6 are supported)"),
                })
                continue
            sources.append(SourceFile(
                source_path=path,
                relative_path=relative,
                output_path=output_path,
                channels=channels,
                sample_rate=sample_rate,
            ))
    if not sources and not pre_errors:
        raise DiscoveryError(f"no WAV/FLAC files found under {input_dir}")
    return sources, pre_errors
