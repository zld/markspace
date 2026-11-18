"""Format sniffing from signatures and container structure.

Extensions are a hint only: :func:`sniff_path` reads the leading bytes and,
when they look like ISO BMFF, validates the first box chain before trusting
it.  Unknown or truncated inputs raise :class:`ScanFailure` with an offset.
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from typing import BinaryIO

from . import bmff
from .errors import (
    ERR_FILE_TOO_LARGE,
    ERR_IO,
    ERR_PERMISSION,
    ERR_TRUNCATED,
    ERR_UNKNOWN_FORMAT,
    ScanFailure,
)
from .limits import Limits

WAV = "wav"
AIFF = "aiff"
BMFF = "bmff"

_CONTAINER_BRANDS = ("mp41", "mp42", "isom", "iso2", "iso4", "iso5", "iso6",
                     "M4A ", "M4B ", "M4V ", "qt  ", "avc1", "dash", "mmp4",
                     "MSNV", "f4v ")


@dataclass(frozen=True)
class SniffResult:
    kind: str  # "wav" | "aiff" | "bmff"
    container: str  # "wav" | "rf64" | "aiff" | "aifc" | "mp4" | "m4a" | "mov"
    brands: tuple[str, ...] = ()
    extension_hint: str = ""
    extension_matches: bool | None = None

    def as_dict(self) -> dict:
        return {
            "kind": self.kind,
            "container": self.container,
            "brands": list(self.brands),
            "extension_hint": self.extension_hint,
            "extension_matches": self.extension_matches,
        }


_EXTENSIONS = {
    ".wav": {"wav", "rf64"},
    ".wave": {"wav", "rf64"},
    ".aif": {"aiff", "aifc"},
    ".aiff": {"aiff", "aifc"},
    ".aifc": {"aiff", "aifc"},
    ".mp4": {"mp4", "m4a", "mov"},
    ".m4a": {"mp4", "m4a", "mov"},
    ".m4v": {"mp4", "m4a", "mov"},
    ".mov": {"mp4", "m4a", "mov"},
}


def _ext(path: str, containers: set[str]) -> tuple[str, bool | None]:
    ext = os.path.splitext(path)[1].lower()
    allowed = _EXTENSIONS.get(ext)
    if allowed is None:
        return ext, None
    return ext, bool(allowed & containers)


def sniff_path(path: str, limits: Limits | None = None) -> SniffResult:
    limits = limits or Limits()
    try:
        size = os.path.getsize(path)
    except PermissionError as exc:
        raise ScanFailure(ERR_PERMISSION, f"permission denied: {path}", 0) from exc
    except OSError as exc:
        raise ScanFailure(ERR_IO, f"cannot stat file: {exc}", 0) from exc
    if size > limits.max_file_size:
        raise ScanFailure(
            ERR_FILE_TOO_LARGE,
            f"file size {size} exceeds limit {limits.max_file_size}",
            0,
        )
    try:
        with open(path, "rb") as fh:
            return _sniff_handle(fh, size, path, limits)
    except PermissionError as exc:
        raise ScanFailure(ERR_PERMISSION, f"permission denied: {path}", 0) from exc
    except OSError as exc:
        raise ScanFailure(ERR_IO, f"cannot read file: {exc}", 0) from exc


def _sniff_handle(fh: BinaryIO, size: int, path: str, limits: Limits) -> SniffResult:
    head = fh.read(16)
    if len(head) < 8:
        raise ScanFailure(
            ERR_TRUNCATED,
            f"file is too short for a container signature ({len(head)} bytes)",
            0,
        )
    if head[0:4] in (b"RIFF", b"RF64"):
        if len(head) < 12:
            raise ScanFailure(ERR_TRUNCATED, "RIFF header is truncated", 0)
        if head[8:12] != b"WAVE":
            raise ScanFailure(ERR_UNKNOWN_FORMAT, "RIFF file is not WAVE", 8)
        container = "rf64" if head[0:4] == b"RF64" else "wav"
        hint, matches = _ext(path, {container})
        return SniffResult("wav", container, (), hint, matches)
    if head[0:4] == b"FORM":
        if len(head) < 12:
            raise ScanFailure(ERR_TRUNCATED, "FORM header is truncated", 0)
        form = head[8:12]
        if form not in (b"AIFF", b"AIFC"):
            raise ScanFailure(ERR_UNKNOWN_FORMAT, "FORM file is not AIFF/AIFC", 8)
        container = "aifc" if form == b"AIFC" else "aiff"
        hint, matches = _ext(path, {"aiff", "aifc"})
        return SniffResult("aiff", container, (), hint, matches)
    brands = _bmff_brands(fh, size, limits)
    container = _bmff_container(brands, path)
    hint, matches = _ext(path, {"mp4", "m4a", "mov"})
    return SniffResult("bmff", container, brands, hint, matches)


def _bmff_container(brands: tuple[str, ...], path: str) -> str:
    lowered = {b.strip().lower() for b in brands}
    if "qt  " in lowered:
        return "mov"
    if lowered & {"m4a ", "m4b "}:
        return "m4a"
    if lowered & {"m4v ", "m4p "}:
        return "mp4"
    if lowered & set(_CONTAINER_BRANDS):
        return "mp4"
    ext = os.path.splitext(path)[1].lower()
    if ext == ".mov":
        return "mov"
    if ext == ".m4a":
        return "m4a"
    return "mp4"


def _bmff_brands(fh: BinaryIO, size: int, limits: Limits) -> tuple[str, ...]:
    fh.seek(0)
    head = fh.read(8)
    box_size = int.from_bytes(head[0:4], "big")
    btype = head[4:8]
    if btype not in (b"ftyp", b"moov", b"free", b"skip", b"wide", b"mdat"):
        raise ScanFailure(
            ERR_UNKNOWN_FORMAT,
            f"unrecognised container signature {btype!r}",
            4,
        )
    header = 8
    if box_size == 1:
        fh.seek(8)
        ext = fh.read(8)
        if len(ext) != 8:
            raise ScanFailure(ERR_TRUNCATED, "extended box size is truncated", 8)
        box_size = int.from_bytes(ext, "big")
        header = 16
    if box_size == 0:
        if btype != b"mdat":
            raise ScanFailure(
                ERR_UNKNOWN_FORMAT, "zero-sized leading box", 0
            )
        box_size = size
    if box_size < header:
        raise ScanFailure(
            ERR_UNKNOWN_FORMAT, f"first box size {box_size} is invalid", 0
        )
    if box_size > size:
        raise ScanFailure(
            ERR_TRUNCATED,
            f"first box size {box_size} exceeds file size {size}",
            0,
        )
    brands: tuple[str, ...] = ()
    if btype == b"ftyp":
        fh.seek(header)
        payload = fh.read(min(box_size - header, 256))
        if len(payload) < 8:
            raise ScanFailure(ERR_TRUNCATED, "ftyp payload is truncated", header)
        major = payload[0:4].decode("latin-1").strip("\x00")
        found = [major or "unknown"]
        for i in range(8, len(payload) - 3, 4):
            brand = payload[i : i + 4].decode("latin-1").strip("\x00")
            if brand and brand not in found:
                found.append(brand)
        brands = tuple(found)
    # Structural sanity: a garbage box tree must not masquerade as an MP4.
    bmff.BoxReader(fh, size, limits).parse()
    return brands
