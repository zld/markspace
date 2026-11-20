"""Content-based format sniffing and parallel extraction dispatch.

Files are identified by magic bytes, never by extension. Extraction is
parallel across files; per-file failures are captured, not raised, so one
bad asset never loses the others' results.
"""
from __future__ import annotations

import os
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from .model import Inconsistency, MediaError, MediaInfo
from .mp4 import parse_mp4
from .util import hash_and_read, safe_join
from .wav import parse_wav

SUPPORTED_FORMATS = ("mp4", "wav")


def sniff_format(head: bytes) -> str | None:
    if len(head) >= 12 and head[:4] == b"RIFF" and head[8:12] == b"WAVE":
        return "wav"
    if len(head) >= 8 and head[4:8] == b"ftyp":
        return "mp4"
    return None


def extract_file(root: Path, rel: str) -> tuple[MediaInfo, list[Inconsistency]]:
    """Extract one asset. Raises MediaError on fatal per-file problems."""
    full = safe_join(root, rel)
    if not full.is_file():
        raise MediaError("missing-file", f"{rel}: file not found under input directory")
    digest, size, data = hash_and_read(full, rel)
    fmt = sniff_format(data[:16])
    if fmt is None:
        raise MediaError("unrecognized-format", f"{rel}: content matches no supported container (mp4/wav)")
    if fmt == "mp4":
        tracks, problems = parse_mp4(data, rel)
    else:
        tracks, problems = parse_wav(data, rel)
    return MediaInfo(rel, fmt, size, digest, tracks), problems


def extract_many(root: Path, rels: list[str]) -> dict[str, tuple[MediaInfo | None, list[Inconsistency]]]:
    """Extract all assets in parallel. Every rel gets an entry: on fatal
    failure the MediaInfo is None and the inconsistency carries the error."""
    out: dict[str, tuple[MediaInfo | None, list[Inconsistency]]] = {}
    workers = min(8, (os.cpu_count() or 4), max(1, len(rels)))
    with ThreadPoolExecutor(max_workers=workers) as pool:
        futures = {rel: pool.submit(extract_file, root, rel) for rel in rels}
        for rel, fut in futures.items():
            try:
                media, problems = fut.result()
                out[rel] = (media, problems)
            except MediaError as e:
                out[rel] = (None, [Inconsistency(code=e.code, path=rel, message=e.message, offset=e.offset)])
    return out
