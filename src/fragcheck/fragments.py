"""Recover fragment boundaries from the top-level moof/mdat sequence.

A fragment is a moof box plus every mdat that immediately follows it.
A moof with no following mdat (e.g. a recorder crash between the two)
is kept as an *unclosed* fragment with an explicit note; callers decide
whether that is tolerable (validation reports it, repair refuses it).
"""
from __future__ import annotations

import struct
from dataclasses import dataclass


@dataclass
class Fragment:
    index: int
    moof_offset: int
    start_offset: int
    end_offset: int
    closed: bool
    note: str
    tfdt: int | None  # baseMediaDecodeTime of the first traf, if present

    @property
    def byte_length(self):
        return self.end_offset - self.start_offset


def parse_tfdt(data, moof_box):
    """baseMediaDecodeTime from the first traf/tfdt inside a moof, or None."""
    for traf in (c for c in moof_box.children if c.type == "traf"):
        for tfdt in (c for c in traf.children if c.type == "tfdt"):
            payload = data[tfdt.data_offset:tfdt.end]
            if len(payload) < 4:
                return None
            version = payload[0]
            need = 12 if version == 1 else 8
            if len(payload) < need:
                return None
            fmt = ">Q" if version == 1 else ">I"
            return struct.unpack_from(fmt, payload, 4)[0]
    return None


def scan_fragments(top_boxes, data=None):
    """Group top-level boxes into fragments; returns a list of Fragment."""
    fragments = []
    pending = None

    def flush():
        nonlocal pending
        if pending is not None:
            fragments.append(pending)
            pending = None

    for box in top_boxes:
        if box.type == "moof":
            flush()
            pending = Fragment(
                index=len(fragments),
                moof_offset=box.offset,
                start_offset=box.offset,
                end_offset=box.end,
                closed=False,
                note="",
                tfdt=parse_tfdt(data, box) if data is not None else None,
            )
        elif box.type == "mdat" and pending is not None:
            pending.end_offset = box.end
            pending.closed = True
        else:
            if pending is not None and not pending.closed:
                pending.note = f"moof at {pending.moof_offset} not followed by mdat"
            flush()
    if pending is not None and not pending.closed:
        pending.note = "unclosed fragment at end of file"
    flush()
    return fragments


def orphan_mdats(top_boxes, fragments):
    """Top-level mdat boxes not absorbed into any fragment."""
    covered = [(f.start_offset, f.end_offset) for f in fragments]
    orphans = []
    for box in top_boxes:
        if box.type != "mdat":
            continue
        if not any(s <= box.offset and box.end <= e for s, e in covered):
            orphans.append(box)
    return orphans
