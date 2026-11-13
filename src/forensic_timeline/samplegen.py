"""Deterministic local sample generator.

Creates a block-structured sample image plus its acquisition manifest inside a
directory the caller (normally a temporary directory) provides. The generator
never writes outside that directory and never touches real disks, partitions
or external evidence sources.

Sample layout produced by :func:`generate_sample`:

* 11 unique events + 1 exact duplicate + 1 malformed record line
  (=> 12 valid events, 1 rejected record)
* device clock running ``drift_seconds`` ahead of true UTC (default +37s),
  recoverable from 3 anchor events listed in the manifest
* one 1800 s gap between consecutive events (=> 1 missing interval with the
  default 900 s threshold); head/tail margins are 60 s
* 2 blocks corrupted after the manifest was written (=> 2 hash mismatches)
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path

FORMAT_TAG = "forensic-timeline-sample/1"
BLOCK_SIZE = 512

_BASE = datetime(2026, 3, 1, 0, 0, 0, tzinfo=timezone.utc)
# True-UTC offsets (seconds) of the 11 unique events: 300 s spacing with one
# 1800 s hole between 1500 and 3300.
_EVENT_OFFSETS = [0, 300, 600, 900, 1200, 1500, 3300, 3600, 3900, 4200, 4500]
_EVENT_TYPES = [
    "file_write", "process_start", "file_delete", "login", "file_write",
    "registry_set", "network_conn", "file_write", "process_exit",
    "file_read", "logout",
]
_ACQ_MARGIN_SECONDS = 60
_LAB_TZ = timezone(timedelta(hours=8))  # device wall clock: UTC+08:00


@dataclass
class SampleSpec:
    image_path: Path
    manifest_path: Path
    event_count: int
    rejected_records: int
    hash_mismatches: int
    missing_intervals: int
    drift_seconds: int
    corrupted_blocks: list[int]


def _utc_iso(dt: datetime) -> str:
    return dt.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def generate_sample(dest_dir: str | Path, drift_seconds: int = 37) -> SampleSpec:
    dest = Path(dest_dir)
    # Hard containment: the generator only writes inside the directory the
    # caller provides (normally a freshly created temporary directory).
    if not dest.is_absolute():
        raise ValueError("sample destination must be an absolute directory path")
    dest.mkdir(parents=True, exist_ok=True)
    if not dest.is_dir():
        raise ValueError(f"sample destination {dest} is not a directory")

    true_times = [_BASE + timedelta(seconds=off) for off in _EVENT_OFFSETS]

    records: list[dict] = []
    for i, true in enumerate(true_times):
        recorded = true + timedelta(seconds=drift_seconds)  # device clock ahead
        records.append({
            "id": f"evt-{i:03d}",
            "ts": recorded.astimezone(_LAB_TZ).isoformat(),
            "type": _EVENT_TYPES[i],
            "desc": f"sample event {i} on lab host",
        })
    records.append(dict(records[3]))  # exact duplicate of evt-003

    lines = [json.dumps(r, sort_keys=True).encode("utf-8") for r in records]
    # One malformed record line (truncated JSON) -> rejected at read time.
    lines.append(b'{"id":"evt-broken","ts":"2026-03-01T08:05:37+08:00","type":')

    # Pack record lines into fixed-size blocks, zero padded.
    blocks: list[bytes] = []
    cur = bytearray()
    for line in lines:
        piece = line + b"\n"
        if len(piece) > BLOCK_SIZE:
            raise ValueError("record line does not fit into one block")
        if len(cur) + len(piece) > BLOCK_SIZE:
            blocks.append(bytes(cur))
            cur = bytearray()
        cur += piece
    if cur:
        blocks.append(bytes(cur))
    padded = [b + b"\x00" * (BLOCK_SIZE - len(b)) for b in blocks]

    # Manifest reflects the acquisition-time (pre-corruption) state.
    manifest = {
        "format": FORMAT_TAG,
        "block_size": BLOCK_SIZE,
        "block_count": len(padded),
        "image_sha256": hashlib.sha256(b"".join(padded)).hexdigest(),
        "acquisition_start_utc": _utc_iso(_BASE - timedelta(seconds=_ACQ_MARGIN_SECONDS)),
        "acquisition_end_utc": _utc_iso(true_times[-1] + timedelta(seconds=_ACQ_MARGIN_SECONDS)),
        "blocks": [
            {
                "index": i,
                "offset": i * BLOCK_SIZE,
                "length": BLOCK_SIZE,
                "sha256": hashlib.sha256(blk).hexdigest(),
            }
            for i, blk in enumerate(padded)
        ],
        "anchors": [
            {"event_id": "evt-000", "true_utc": _utc_iso(true_times[0])},
            {"event_id": "evt-005", "true_utc": _utc_iso(true_times[5])},
            {"event_id": "evt-010", "true_utc": _utc_iso(true_times[10])},
        ],
    }

    # Post-acquisition tampering: alter one description byte inside two blocks.
    # The change stays inside a JSON string value, so records still parse and
    # only the block hashes diverge from the manifest.
    corrupted = sorted({1, len(padded) - 1})
    tampered = []
    for i, blk in enumerate(padded):
        if i in corrupted:
            pos = blk.index(b"sample")
            blk = blk[:pos] + b"Xample" + blk[pos + len(b"sample"):]
        tampered.append(blk)

    image_path = dest / "sample-image.bin"
    manifest_path = dest / "sample-manifest.json"
    image_path.write_bytes(b"".join(tampered))
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")

    return SampleSpec(
        image_path=image_path,
        manifest_path=manifest_path,
        event_count=len(records),
        rejected_records=1,
        hash_mismatches=len(corrupted),
        missing_intervals=1,
        drift_seconds=drift_seconds,
        corrupted_blocks=corrupted,
    )
