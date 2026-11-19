"""Deterministic sidx rebuild planning and atomic output writing.

Layout rule: every existing sidx box (all must sit before the first
moof) is excised and a freshly built sidx is inserted immediately
before the first fragment. Everything before the first moof keeps its
absolute offset, so stco/co64 chunk offsets in moov stay valid; the
new sidx references are relative to its own end, so the shifted
fragments are still addressed correctly.
"""
from __future__ import annotations

import os
import struct
import tempfile
from dataclasses import dataclass

from .fragments import orphan_mdats


class RepairError(Exception):
    """Repair cannot proceed unambiguously (CLI exit code 3)."""

    def __init__(self, reasons):
        self.reasons = [str(r) for r in reasons]
        super().__init__("; ".join(self.reasons))


@dataclass
class Plan:
    sidx_bytes: bytes
    insert_before: int      # absolute offset of the first moof
    remove_ranges: list     # (start, end) of old sidx boxes, all < insert_before
    durations: list


def build_sidx(reference_id, timescale, ept, durations, byte_lengths):
    """Build a version-1 sidx box referencing consecutive fragments."""
    refs = b"".join(
        struct.pack(">III", length & 0x7FFFFFFF, duration, 0x80000000)
        for length, duration in zip(byte_lengths, durations))
    payload = (b"\x01\x00\x00\x00"
               + struct.pack(">IIQqHH", reference_id, timescale, ept, 0,
                             0, len(durations))
               + refs)
    return struct.pack(">I4s", 8 + len(payload), b"sidx") + payload


def _covered_by_chunks(box, chunk_ranges):
    """True if the box's data range is fully tiled by sample-table chunks."""
    pos = box.data_offset
    for (s, e) in sorted(chunk_ranges):
        if s > pos:
            return False
        if e > pos:
            pos = e
        if pos >= box.end:
            return True
    return pos >= box.end


def plan_rebuild(analysis):
    """Build a deterministic rebuild Plan or raise RepairError with reasons."""
    reasons = []
    for t in analysis.tracks:
        reasons.extend(t.errors)  # contradictory sample counts stop repair

    closed = [f for f in analysis.fragments if f.closed]
    if not closed:
        reasons.append("no complete moof/mdat fragments found")
    for f in analysis.fragments:
        if not f.closed:
            reasons.append(
                f"fragment at offset {f.start_offset} is unclosed "
                f"({f.note}); cannot index it")

    chunk_ranges = []
    for t in analysis.tracks:
        chunk_ranges.extend(zip(t.chunk_offsets,
                                (o + s for o, s in zip(t.chunk_offsets,
                                                       t.chunk_sizes))))
    for box in orphan_mdats(analysis.top_boxes, analysis.fragments):
        if not _covered_by_chunks(box, chunk_ranges):
            reasons.append(
                f"mdat at offset {box.offset} cannot be uniquely "
                f"attributed to a fragment or the sample table")

    first_moof = min((f.moof_offset for f in closed), default=None)
    if first_moof is not None:
        for s in analysis.sidxes:
            if s.offset >= first_moof:
                reasons.append(
                    f"sidx at offset {s.offset} lies inside the fragment "
                    f"region; cannot rebuild without shifting media data")
    reasons.extend(r for r in analysis.sidx_errors
                   if r.startswith("overlapping references"))
    if reasons:
        raise RepairError(reasons)

    tfdts = [f.tfdt for f in closed]
    if any(t is None for t in tfdts):
        raise RepairError(["a fragment lacks tfdt; cannot derive "
                           "subsegment durations deterministically"])
    durations = [tfdts[i + 1] - tfdts[i] for i in range(len(tfdts) - 1)]
    durations.append(durations[-1] if durations else 0)
    if any(d < 0 for d in durations):
        raise RepairError(["fragment tfdt sequence decreases; cannot "
                           "derive non-negative durations"])

    track = analysis.tracks[0] if analysis.tracks else None
    sidx_bytes = build_sidx(
        reference_id=track.track_id if track else 1,
        timescale=track.timescale if track else 1000,
        ept=tfdts[0],
        durations=durations,
        byte_lengths=[f.byte_length for f in closed])
    remove = [(s.offset, s.end) for s in analysis.sidxes]
    return Plan(sidx_bytes=sidx_bytes, insert_before=first_moof,
                remove_ranges=remove, durations=durations)


def apply_plan(analysis, plan, out_path):
    """Write the rebuilt file atomically; the input is never modified."""
    src = os.path.abspath(analysis.path)
    dst = os.path.abspath(out_path)
    if src == dst:
        raise RepairError(["output path equals input path; input files "
                           "are read-only, refusing to overwrite"])
    data = analysis.data
    head, pos = [], 0
    for (s, e) in sorted(plan.remove_ranges):
        head.append(data[pos:s])
        pos = e
    head.append(data[pos:plan.insert_before])
    new_data = b"".join(head) + plan.sidx_bytes + data[plan.insert_before:]

    out_dir = os.path.dirname(dst) or "."
    fd, tmp = tempfile.mkstemp(prefix=".fragcheck-", dir=out_dir)
    try:
        with os.fdopen(fd, "wb") as fh:
            fh.write(new_data)
        os.replace(tmp, dst)
    except BaseException:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise
    return len(new_data)
