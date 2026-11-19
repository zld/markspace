"""Batch scanning, per-file processing, scheduling and aggregation.

This module owns the batch contract:

* :func:`scan_inputs` finds WAV files recursively and returns sorted
  POSIX-style relative paths, so runs and reports are reproducible.
* :func:`validate_paths` refuses overlapping input/output trees, which
  would otherwise let the pipeline overwrite its own source material.
* :func:`process_file` runs the full read -> resample -> measure -> gain
  -> write chain for one file and always returns a record; per-file
  failures are captured in that record and never abort the batch.
* :func:`run_batch` schedules files (sequentially or in a process pool)
  and assembles the report document.
* :func:`verify_report` re-reads the produced WAVs and re-derives the
  reported numbers, for offline auditing of a report.

The per-file measurement strategy is described in :func:`process_file`.
"""

from __future__ import annotations

import concurrent.futures
import json
import math
import os
import time
from dataclasses import dataclass
from typing import Optional

import numpy as np

from .io import (
    PipelineError,
    WavReader,
    WavWriter,
    count_rail_samples,
    probe_wav,
    quantise_pcm,
)
from .loudness import LevelMeter, PEAK_CEILING, decide_gain
from .resample import PolyphaseResampler
from . import report as report_mod
from .report import ReportWriteError

__all__ = [
    "BatchConfig",
    "ConfigError",
    "FileConfig",
    "format_summary",
    "process_file",
    "run_batch",
    "scan_inputs",
    "validate_paths",
    "verify_report",
]

DEFAULT_BLOCK_FRAMES = 65536


class ConfigError(Exception):
    """Batch-level misconfiguration; maps to exit code 2."""


@dataclass(frozen=True)
class FileConfig:
    """Everything needed to process one file."""

    input_dir: str
    output_dir: str
    relative_path: str
    output_rate: int
    output_subtype: str
    target_lufs: float
    max_gain_db: Optional[float]
    max_peak_db: Optional[float]
    block_frames: int = DEFAULT_BLOCK_FRAMES

    @property
    def input_path(self) -> str:
        return os.path.join(self.input_dir, *self.relative_path.split("/"))

    @property
    def output_path(self) -> str:
        return os.path.join(self.output_dir, *self.relative_path.split("/"))


@dataclass(frozen=True)
class BatchConfig:
    """Batch-level options shared by every file."""

    input_dir: str
    output_dir: str
    report_path: str
    output_rate: int
    output_subtype: str = "PCM_24"
    target_lufs: float = -18.0
    max_gain_db: Optional[float] = 12.0
    max_peak_db: Optional[float] = -0.1
    jobs: int = 1
    block_frames: int = DEFAULT_BLOCK_FRAMES


# ---------------------------------------------------------------------------
# Scanning and path safety
# ---------------------------------------------------------------------------

def scan_inputs(input_dir: str) -> list[str]:
    """Return sorted POSIX relative paths of every ``.wav`` under ``input_dir``."""
    if not os.path.isdir(input_dir):
        raise ConfigError(f"input directory does not exist: {input_dir}")
    found: list[str] = []
    for root, dirnames, filenames in os.walk(input_dir, followlinks=False):
        dirnames.sort()
        for name in sorted(filenames):
            if name.lower().endswith(".wav"):
                full = os.path.join(root, name)
                if os.path.isfile(full):
                    rel = os.path.relpath(full, input_dir)
                    found.append(rel.replace(os.sep, "/"))
    found.sort()
    return found


def _resolved(path: str) -> str:
    return os.path.realpath(os.path.abspath(path))


def validate_paths(input_dir: str, output_dir: str) -> None:
    """Reject identical or nested input/output trees."""
    src = _resolved(input_dir)
    dst = _resolved(output_dir)
    if src == dst:
        raise ConfigError("input and output directories must differ")
    common = os.path.commonpath([src, dst])
    if common == src:
        raise ConfigError("output directory must not be inside the input directory")
    if common == dst:
        raise ConfigError("input directory must not be inside the output directory")
