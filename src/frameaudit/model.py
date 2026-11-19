"""Data models shared across decode, reorder, repair and output stages."""

from __future__ import annotations

from dataclasses import dataclass, field, asdict
from fractions import Fraction
from typing import Optional

# Canonical frame type labels. Anything outside I/P/B is reported as OTHER.
FRAME_TYPE_I = "I"
FRAME_TYPE_P = "P"
FRAME_TYPE_B = "B"
FRAME_TYPE_OTHER = "OTHER"
FRAME_TYPES = (FRAME_TYPE_I, FRAME_TYPE_P, FRAME_TYPE_B, FRAME_TYPE_OTHER)


@dataclass(slots=True)
class FrameRecord:
    """One decoded video frame in decode order.

    ``decode_index`` is the zero-based position in which the decoder emitted
    the frame.  ``pts`` is the raw presentation timestamp in units of
    ``time_base`` and may be ``None`` when the container provides none.
    """

    decode_index: int
    frame_type: str
    pts: Optional[int]
    time_base: Optional[Fraction]
    key_frame: bool

    def pts_seconds(self) -> Optional[float]:
        """Return the raw PTS expressed in seconds, or ``None``."""
        if self.pts is None or self.time_base is None:
            return None
        return float(self.pts * self.time_base)


@dataclass(slots=True)
class RepairRecord:
    """Per-frame timestamp repair outcome attached to a normalized frame."""

    decode_index: int
    original_pts: Optional[int]
    repaired_pts_seconds: float
    original_pts_seconds: Optional[float]
    repaired: bool
    reasons: list[str] = field(default_factory=list)

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass(slots=True)
class NormalizedFrame:
    """A frame after reordering and timestamp repair."""

    order_index: int
    decode_index: int
    frame_type: str
    key_frame: bool
    original_pts: Optional[int]
    time_base: Optional[Fraction]
    original_pts_seconds: Optional[float]
    repaired_pts_seconds: float
    timestamp_repaired: bool
    repair_reasons: list[str] = field(default_factory=list)
    reorder_overflow: bool = False

    def to_repair_record(self) -> RepairRecord:
        return RepairRecord(
            decode_index=self.decode_index,
            original_pts=self.original_pts,
            repaired_pts_seconds=self.repaired_pts_seconds,
            original_pts_seconds=self.original_pts_seconds,
            repaired=self.timestamp_repaired,
            reasons=list(self.repair_reasons),
        )


@dataclass(slots=True)
class AuditStats:
    """Aggregated statistics consumed by both terminal and JSON outputs."""

    input_path: str
    container_format: Optional[str]
    video_codec: Optional[str]
    time_base: Optional[str]
    average_rate: Optional[str]
    decoded_frames: int
    frame_type_counts: dict[str, int]
    repaired_timestamps: int
    max_jump_seconds: float
    reorder_overflows: int
    frames: list[NormalizedFrame] = field(default_factory=list)

    def to_dict(self) -> dict:
        return {
            "input": {
                "path": self.input_path,
                "container_format": self.container_format,
                "video_codec": self.video_codec,
                "time_base": self.time_base,
                "average_rate": self.average_rate,
            },
            "decoded_frames": self.decoded_frames,
            "frame_types": dict(self.frame_type_counts),
            "repaired_timestamps": self.repaired_timestamps,
            "max_jump_seconds": round(self.max_jump_seconds, 6),
            "reorder_overflows": self.reorder_overflows,
            "frames": [
                {
                    "order_index": f.order_index,
                    "decode_index": f.decode_index,
                    "frame_type": f.frame_type,
                    "key_frame": f.key_frame,
                    "original_pts": f.original_pts,
                    "time_base": str(f.time_base) if f.time_base else None,
                    "original_pts_seconds": f.original_pts_seconds,
                    "repaired_pts_seconds": round(f.repaired_pts_seconds, 6),
                    "timestamp_repaired": f.timestamp_repaired,
                    "repair_reasons": list(f.repair_reasons),
                    "reorder_overflow": f.reorder_overflow,
                }
                for f in self.frames
            ],
        }
