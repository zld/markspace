"""Per-block and whole-image SHA-256 verification against the manifest.

Each inconsistency category is reported separately: hash mismatch, manifest
entry without an image block, image block missing from the manifest, and
malformed hash values in the manifest are never merged into one failure.
"""

from __future__ import annotations

import re

from .models import BlockInfo, IntegrityResult, IntegrityStatus

HASH_RE = re.compile(r"^[0-9a-fA-F]{64}$")


def verify_blocks(blocks: list[BlockInfo], manifest_blocks: list[dict]) -> list[IntegrityResult]:
    results: list[IntegrityResult] = []
    manifest_by_index: dict[int, dict] = {}
    for entry in manifest_blocks:
        if isinstance(entry, dict) and isinstance(entry.get("index"), int):
            manifest_by_index[entry["index"]] = entry
    actual_by_index = {block.index: block for block in blocks}

    covered: set[int] = set()
    for index in sorted(manifest_by_index):
        entry = manifest_by_index[index]
        expected = entry.get("sha256")
        offset = entry.get("offset") if isinstance(entry.get("offset"), int) else None
        covered.add(index)
        if not isinstance(expected, str) or not HASH_RE.match(expected):
            results.append(IntegrityResult(
                index, offset, repr(expected), None, IntegrityStatus.BAD_HASH_FORMAT))
            continue
        expected = expected.lower()
        actual_block = actual_by_index.get(index)
        if actual_block is None:
            results.append(IntegrityResult(
                index, offset, expected, None, IntegrityStatus.MISSING_BLOCK))
            continue
        status = (IntegrityStatus.OK
                  if actual_block.sha256 == expected else IntegrityStatus.HASH_MISMATCH)
        results.append(IntegrityResult(
            index, actual_block.offset, expected, actual_block.sha256, status))

    for block in blocks:
        if block.index not in covered:
            results.append(IntegrityResult(
                block.index, block.offset, None, block.sha256, IntegrityStatus.EXTRA_BLOCK))
    return results


def whole_image_status(actual_sha256: str, manifest: dict) -> str:
    expected = manifest.get("image_sha256")
    if not isinstance(expected, str) or not HASH_RE.match(expected):
        return "unknown"
    return "match" if actual_sha256 == expected.lower() else "mismatch"
