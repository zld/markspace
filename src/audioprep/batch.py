"""Batch orchestration: discovery, task state, concurrent processing.

The CLI hands a BatchConfig here; each file flows read -> validate -> resample
(kernel plans cached and reused across files) -> measure -> normalize ->
measure -> atomic write. One file's failure never blocks the others, and the
per-file entry records the failing stage and error type for the report.
"""
from __future__ import annotations

import os
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from pathlib import Path

from . import layout, loudness, resample, wavio

DEFAULT_MAX_CHANNELS = 8
DEFAULT_MAX_SECONDS = 1800.0


class BatchError(Exception):
    """Configuration-level failure (exit code 2): bad dirs, no inputs,
    overlapping input/output, or colliding output targets."""


@dataclass(frozen=True)
class BatchConfig:
    input_dir: Path
    output_dir: Path
    rate: int
    target_lufs: float
    max_boost_db: float
    workers: int = 4
    max_channels: int = DEFAULT_MAX_CHANNELS
    max_seconds: float = DEFAULT_MAX_SECONDS

    def params_dict(self) -> dict:
        return {
            "rate": self.rate,
            "target_lufs": self.target_lufs,
            "max_boost_db": self.max_boost_db,
            "workers": self.workers,
            "max_channels": self.max_channels,
            "max_seconds": self.max_seconds,
            "peak_ceiling_db": loudness.PEAK_CEILING_DB,
            "oversample_factor": loudness.OVERSAMPLE_FACTOR,
            "taps_per_phase": resample.TAPS_PER_PHASE,
            "kaiser_beta": resample.KAISER_BETA,
        }


def discover(input_dir: Path) -> tuple[list[Path], list[dict]]:
    """Recursively collect *.wav (case-insensitive) under input_dir.

    Directory symlinks are followed, but a link whose real path is already an
    ancestor is a loop: it is reported with its path and skipped, and
    discovery continues elsewhere.
    """
    files: list[Path] = []
    warnings: list[dict] = []

    def walk(directory: Path, ancestors: frozenset) -> None:
        try:
            entries = sorted(os.scandir(directory), key=lambda e: e.name)
        except OSError as exc:
            warnings.append({"path": str(directory),
                             "reason": "unreadable_directory",
                             "error": str(exc)})
            return
        for entry in entries:
            p = Path(entry.path)
            try:
                if entry.is_dir(follow_symlinks=True):
                    real = os.path.realpath(p)
                    if real in ancestors:
                        warnings.append({"path": str(p),
                                         "reason": "symlink_loop"})
                        continue
                    walk(p, ancestors | {real})
                elif entry.is_file(follow_symlinks=True) and \
                        p.suffix.lower() == ".wav":
                    files.append(p)
            except OSError as exc:
                warnings.append({"path": str(p), "reason": "stat_failed",
                                 "error": str(exc)})

    walk(input_dir, frozenset({os.path.realpath(input_dir)}))
    return files, warnings


def _check_dirs(cfg: BatchConfig) -> None:
    if not cfg.input_dir.is_dir():
        raise BatchError(f"input directory does not exist: {cfg.input_dir}")
    real_in = os.path.realpath(cfg.input_dir)
    real_out = os.path.realpath(cfg.output_dir)
    if real_in == real_out or real_out.startswith(real_in + os.sep) or \
            real_in.startswith(real_out + os.sep):
        raise BatchError(
            f"input and output directories overlap: {real_in} vs {real_out}")


def _targets(files: list[Path], cfg: BatchConfig) -> dict[Path, Path]:
    """Map each input to its output path, preserving relative structure."""
    mapping: dict[Path, Path] = {}
    seen: dict[Path, Path] = {}
    for f in files:
        rel = f.relative_to(cfg.input_dir)
        out_rel = rel.with_suffix(".wav")
        if out_rel in seen:
            raise BatchError(
                f"duplicate output target {out_rel}: {seen[out_rel]} and {f}")
        seen[out_rel] = f
        mapping[f] = cfg.output_dir / out_rel
    return mapping


@dataclass
class BatchRunner:
    cfg: BatchConfig
    _plans: dict = field(default_factory=dict)
    _lock: threading.Lock = field(default_factory=threading.Lock)

    def plan_for(self, in_rate: int) -> resample.ResamplePlan:
        key = (in_rate, self.cfg.rate)
        with self._lock:
            plan = self._plans.get(key)
            if plan is None:
                plan = resample.plan_resample(in_rate, self.cfg.rate)
                self._plans[key] = plan
            return plan

    def process_one(self, src: Path, dst: Path) -> dict:
        cfg = self.cfg
        entry = {"relpath": str(src.relative_to(cfg.input_dir)),
                 "status": "failed", "stage": None,
                 "error_type": None, "error": None,
                 "input_rate": None, "output_rate": None, "channels": None,
                 "input_frames": None, "output_frames": None,
                 "padded_frames": None, "gain_db": None,
                 "normalizable": None, "measured_frames": None,
                 "input_rms_dbfs": None, "input_lufs": None,
                 "input_true_peak_dbtp": None, "input_clipped_samples": None,
                 "output_rms_dbfs": None, "output_lufs": None,
                 "output_true_peak_dbtp": None, "output_clipped_samples": None}
        stage = "read"
        try:
            data, in_rate = wavio.read_wav(src, cfg.max_channels,
                                           cfg.max_seconds)
            frames = layout.ensure_2d(data)
            entry.update(input_rate=in_rate, output_rate=cfg.rate,
                         channels=frames.shape[1],
                         input_frames=frames.shape[0])

            stage = "resample"
            plan = self.plan_for(in_rate)
            padded, n_pad = layout.pad_to_block(frames, plan.down)
            channels = [resample.apply_plan(plan, col)
                        for col in layout.deinterleave(padded)]
            out = layout.interleave(channels)
            n_out = resample.output_frame_count(frames.shape[0], in_rate,
                                                cfg.rate)
            out = layout.crop_frames(out, n_out)
            entry.update(output_frames=n_out, padded_frames=n_pad)

            stage = "measure"
            pre = loudness.measure(out, cfg.rate)
            gain, normalizable = loudness.normalization_gain(
                pre["lufs"], pre["true_peak_dbtp"], cfg.target_lufs,
                cfg.max_boost_db)
            if gain != 0.0:
                out = out * (10.0 ** (gain / 20.0))
            post = loudness.measure(out, cfg.rate)
            entry.update(
                gain_db=gain, normalizable=normalizable,
                measured_frames=post["measured_frames"],
                input_rms_dbfs=pre["rms_dbfs"], input_lufs=pre["lufs"],
                input_true_peak_dbtp=pre["true_peak_dbtp"],
                input_clipped_samples=pre["clipped_samples"],
                output_rms_dbfs=post["rms_dbfs"], output_lufs=post["lufs"],
                output_true_peak_dbtp=post["true_peak_dbtp"],
                output_clipped_samples=post["clipped_samples"])

            stage = "write"
            wavio.write_wav_atomic(dst, out, cfg.rate)
            entry.update(status="ok", stage=None)
        except Exception as exc:  # per-file failure must not stop the batch
            entry.update(stage=stage, error_type=type(exc).__name__,
                         error=str(exc))
        return entry

    def run(self, on_file=None) -> tuple[list[dict], list[dict]]:
        """Process all discovered files; returns (entries, warnings)."""
        cfg = self.cfg
        _check_dirs(cfg)
        files, warnings = discover(cfg.input_dir)
        if not files:
            raise BatchError(f"no WAV files found under {cfg.input_dir}")
        targets = _targets(files, cfg)
        entries: list[dict] = []
        with ThreadPoolExecutor(max_workers=max(1, cfg.workers)) as pool:
            futures = {pool.submit(self.process_one, f, targets[f]): f
                       for f in files}
            for fut in as_completed(futures):
                entry = fut.result()
                entries.append(entry)
                if on_file is not None:
                    on_file(entry)
        return entries, warnings
