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
        raise WriteError(
            f"cannot create output directory {path}: {exc}"
        ) from exc


def _strip_peak_chunk(raw: bytes) -> bytes:
    """Remove the libsndfile PEAK chunk from a RIFF/WAV byte string.

    The PEAK chunk holds runtime peak metadata whose storage is not
    byte-stable across runs even for identical PCM, so dropping it makes
    rendered WAV files reproducible byte-for-byte.  All other chunks and
    the PCM payload are preserved untouched.
    """
    if raw[:4] != b"RIFF" or raw[8:12] != b"WAVE":
        return raw
    body = bytearray()  # chunks only; the 4-byte "WAVE" id is excluded
    pos = 12
    while pos + 8 <= len(raw):
        chunk_id = raw[pos:pos + 4]
        size = int.from_bytes(raw[pos + 4:pos + 8], "little")
        padded = size + (size & 1)
        if chunk_id != b"PEAK":
            body += raw[pos:pos + 8 + padded]
        pos += 8 + padded
    # RIFF size counts every byte after the 8-byte RIFF/size header,
    # i.e. the "WAVE" id (4) plus all retained chunks.
    riff_size = 4 + len(body)
    return b"RIFF" + riff_size.to_bytes(4, "little") + b"WAVE" + bytes(body)


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
    """Write a float32 WAV atomically; overloads above 1.0 are preserved.

    The auto-generated PEAK chunk is stripped so identical audio yields
    byte-identical files across runs.
    """
    refuse_overwrite(path, force)
    ensure_output_dir(path.parent)
    try:
        fd, tmp_name = tempfile.mkstemp(
            prefix=f".{path.name}.", suffix=".tmp", dir=str(path.parent)
        )
    except OSError as exc:
        raise WriteError(f"failed to create temporary file for {path}: {exc}") from exc
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
        raw = tmp_path.read_bytes()
        tmp_path.write_bytes(_strip_peak_chunk(raw))
        os.replace(tmp_path, path)
    except (OSError, sf.LibsndfileError, RuntimeError) as exc:
        tmp_path.unlink(missing_ok=True)
        raise WriteError(f"failed to write WAV {path}: {exc}") from exc


def write_json(path: Path, payload: dict, force: bool) -> None:
    """Write a JSON report atomically with stable key ordering."""
    refuse_overwrite(path, force)
    ensure_output_dir(path.parent)
    try:
        fd, tmp_name = tempfile.mkstemp(
            prefix=f".{path.name}.", suffix=".tmp", dir=str(path.parent)
        )
    except OSError as exc:
        raise WriteError(f"failed to create temporary file for {path}: {exc}") from exc
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
