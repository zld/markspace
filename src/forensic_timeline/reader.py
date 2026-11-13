"""Read-only streaming image reader and manifest loader.

Only self-generated sample images are accepted: the manifest must carry the
sample format tag, and the image must be a regular file. The image is opened
read-only and processed block by block so it is never fully loaded into
memory. A single bad record is counted as an anomaly and never discards the
remaining valid records.
"""

from __future__ import annotations

import hashlib
import json
import stat
from dataclasses import dataclass, field
from pathlib import Path

from .models import Anomaly, BlockInfo, RawRecord
from .samplegen import FORMAT_TAG

MIN_BLOCK_SIZE = 128
MAX_BLOCK_SIZE = 1 << 20


class ImageReadError(Exception):
    """Raised for unreadable/invalid images or manifests (exit code 3)."""


@dataclass
class ReadResult:
    blocks: list[BlockInfo]
    records: list[RawRecord]
    anomalies: list[Anomaly] = field(default_factory=list)
    whole_sha256: str = ""
    size: int = 0


def load_manifest(path: str | Path) -> dict:
    p = Path(path)
    try:
        text = p.read_text(encoding="utf-8")
    except OSError as exc:
        raise ImageReadError(f"cannot read manifest {p}: {exc}") from exc
    try:
        data = json.loads(text)
    except json.JSONDecodeError as exc:
        raise ImageReadError(f"manifest {p} is not valid JSON: {exc}") from exc
    if not isinstance(data, dict) or data.get("format") != FORMAT_TAG:
        raise ImageReadError(
            f"manifest {p} is not a self-generated sample manifest "
            f"(format tag {FORMAT_TAG!r} missing); refusing external evidence sources"
        )
    block_size = data.get("block_size")
    if not isinstance(block_size, int) or not MIN_BLOCK_SIZE <= block_size <= MAX_BLOCK_SIZE:
        raise ImageReadError(f"manifest block_size {block_size!r} out of supported range")
    for key in ("blocks", "anchors", "acquisition_start_utc", "acquisition_end_utc",
                "image_sha256"):
        if key not in data:
            raise ImageReadError(f"manifest missing required key {key!r}")
    if not isinstance(data["blocks"], list) or not isinstance(data["anchors"], list):
        raise ImageReadError("manifest 'blocks' and 'anchors' must be lists")
    return data


def read_image(
    path: str | Path,
    *,
    block_size: int,
    max_blocks: int,
    max_image_bytes: int,
    max_records: int,
) -> ReadResult:
    p = Path(path)
    try:
        st = p.stat()
    except OSError as exc:
        raise ImageReadError(f"cannot stat image {p}: {exc}") from exc
    if not stat.S_ISREG(st.st_mode):
        raise ImageReadError(
            f"refusing non-regular file {p}: only self-generated sample files "
            f"are processed, never disks, partitions or devices"
        )
    size = st.st_size
    if size == 0:
        raise ImageReadError(f"image {p} is empty")
    if size > max_image_bytes:
        raise ImageReadError(f"image size {size} exceeds limit {max_image_bytes}")

    anomalies: list[Anomaly] = []
    nblocks, remainder = divmod(size, block_size)
    if remainder:
        anomalies.append(Anomaly(
            "truncated_image",
            f"{remainder} trailing byte(s) beyond the last full block ignored",
            None,
            nblocks * block_size,
        ))
    if nblocks == 0:
        raise ImageReadError(f"image {p} is smaller than one block ({block_size} bytes)")
    if nblocks > max_blocks:
        raise ImageReadError(f"block count {nblocks} exceeds limit {max_blocks}")

    blocks: list[BlockInfo] = []
    records: list[RawRecord] = []
    whole = hashlib.sha256()
    seq = 0

    try:
        fh = p.open("rb")  # strictly read-only
    except OSError as exc:
        raise ImageReadError(f"cannot open image {p}: {exc}") from exc

    with fh:
        for index in range(nblocks):
            try:
                data = fh.read(block_size)
            except OSError as exc:
                raise ImageReadError(
                    f"read failed at block {index} (offset {index * block_size}): {exc}"
                ) from exc
            if len(data) < block_size:
                anomalies.append(Anomaly(
                    "truncated_image",
                    f"short read at block {index}: got {len(data)} of {block_size} bytes",
                    index,
                    index * block_size,
                ))
                break
            whole.update(data)
            blocks.append(BlockInfo(index, index * block_size, block_size,
                                    hashlib.sha256(data).hexdigest()))

            pos = 0
            for line in data.split(b"\n"):
                start = pos
                pos += len(line) + 1
                stripped = line.strip(b"\x00").strip()
                if not stripped:
                    continue
                offset = index * block_size + start
                try:
                    text = stripped.decode("utf-8")
                except UnicodeDecodeError:
                    anomalies.append(Anomaly(
                        "undecodable_record", "record is not valid UTF-8", index, offset))
                    continue
                try:
                    obj = json.loads(text)
                except json.JSONDecodeError as exc:
                    anomalies.append(Anomaly(
                        "rejected_record", f"record is not valid JSON: {exc}", index, offset))
                    continue
                if (not isinstance(obj, dict)
                        or not isinstance(obj.get("id"), str)
                        or not isinstance(obj.get("ts"), str)):
                    anomalies.append(Anomaly(
                        "rejected_record", "record missing required string fields id/ts",
                        index, offset))
                    continue
                records.append(RawRecord(
                    event_id=obj["id"],
                    raw_timestamp=obj["ts"],
                    event_type=str(obj.get("type", "unknown")),
                    description=str(obj.get("desc", "")),
                    source_block=index,
                    source_offset=offset,
                    seq=seq,
                ))
                seq += 1
                if len(records) > max_records:
                    raise ImageReadError(f"record count exceeds limit {max_records}")

    return ReadResult(
        blocks=blocks,
        records=records,
        anomalies=anomalies,
        whole_sha256=whole.hexdigest(),
        size=size,
    )
