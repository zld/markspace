"""Atomic, refusal-to-overwrite file output helpers.

Outputs are written to a temporary file in the destination directory and
renamed into place only after a successful flush.  Any failure removes
the temporary file so a crashed render never leaves a half-written WAV
or JSON behind.  Existing destinations are rejected unless ``force`` is
set.
"""

from __future__ import annotations

import json
import os
import tempfile
from pathlib import Path

import numpy as np
import soundfile as sf

from .errors import WriteError


def ensure_output_dir(path: Path) -> None:
    try:
        path.mkdir(parents=True, exist_ok=True)
    except OSError as exc:
        raise WriteError(f"cannot create output directory {path}: {exc}") from exc


def refuse_overwrite(path: Path, force: bool) -> None:
    if path.exists() and not force:
        raise WriteError(
            f"output already exists: {path} (use --force to overwrite)"
        )


def write_wav(
    path: Path,
    samples: np.ndarray,
    sample_rate: int,
    force: bool,
) -> None:
    """Write a float32 WAV atomically; overloads above 1.0 are preserved."""
    refuse_overwrite(path, force)
    ensure_output_dir(path.parent)
    fd, tmp_name = tempfile.mkstemp(
        prefix=f".{path.name}.", suffix=".tmp", dir=str(path.parent)
    )
    os.close(fd)
    tmp_path = Path(tmp_name)
    try:
        sf.write(
            str(tmp_path),
            samples.astype(np.float32, copy=False),
            sample_rate,
            subtype="FLOAT",
            format="WAV",
        )
        os.replace(tmp_path, path)
    except (OSError, sf.LibsndfileError, RuntimeError) as exc:
        tmp_path.unlink(missing_ok=True)
        raise WriteError(f"failed to write WAV {path}: {exc}") from exc


def write_json(path: Path, payload: dict, force: bool) -> None:
    """Write a JSON report atomically with stable key ordering."""
    refuse_overwrite(path, force)
    ensure_output_dir(path.parent)
    fd, tmp_name = tempfile.mkstemp(
        prefix=f".{path.name}.", suffix=".tmp", dir=str(path.parent)
    )
    os.close(fd)
    tmp_path = Path(tmp_name)
    try:
        with open(tmp_path, "w", encoding="utf-8") as handle:
            json.dump(payload, handle, indent=2, sort_keys=True)
            handle.write("\n")
        os.replace(tmp_path, path)
    except OSError as exc:
        tmp_path.unlink(missing_ok=True)
        raise WriteError(f"failed to write JSON {path}: {exc}") from exc
