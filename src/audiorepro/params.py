"""Validated, hashable parameters for a processing run."""

from __future__ import annotations

import hashlib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from . import __version__
from .loudness import channel_weights
from .report import canonical_json
from .resample import ResamplerConfig

OUTPUT_SUBTYPE = "FLOAT"
OUTPUT_FORMAT = "WAV"


@dataclass(frozen=True)
class PipelineParams:
    input_dir: Path
    output_dir: Path
    sample_rate: int
    target_lufs: float
    peak_dbfs: float
    jobs: int = 1
    resume: bool = False
    chunk_frames: int = 16384
    resampler: ResamplerConfig = field(default_factory=ResamplerConfig)

    def processing_dict(self) -> dict[str, Any]:
        """Algorithm-relevant parameters; excludes paths, jobs and resume."""
        return {
            "tool_version": __version__,
            "sample_rate": self.sample_rate,
            "target_lufs": self.target_lufs,
            "peak_dbfs": self.peak_dbfs,
            "output_format": OUTPUT_FORMAT,
            "output_subtype": OUTPUT_SUBTYPE,
            "chunk_frames": self.chunk_frames,
            "resampler": self.resampler.as_dict(),
            "loudness": {
                "standard": "ITU-R BS.1770-4",
                "block_ms": 400,
                "hop_ms": 100,
                "absolute_gate_lufs": -70.0,
                "relative_gate_lu": -10.0,
                "channel_weights": {
                    "1": list(channel_weights(1)),
                    "2": list(channel_weights(2)),
                    "6": list(channel_weights(6)),
                },
            },
        }

    def params_hash(self) -> str:
        payload = canonical_json(self.processing_dict()).encode("utf-8")
        return hashlib.sha256(payload).hexdigest()
