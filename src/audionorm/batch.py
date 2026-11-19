"""Directory scanning, task scheduling, and per-file processing pipeline.

Single-file and batch processing share this one pipeline, which in turn
uses the single resampling kernel in resampler.py.
"""
from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
from pathlib import Path

from . import loudness, report
from .errors import E_CONFLICT, E_EXISTS, AudioNormError
from .resampler import estimate_true_peak, output_length, resample
from .wavio import read_wav, write_wav_atomic

TRUE_PEAK_CEILING = 10 ** (-1.0 / 20.0)  # -1 dBTP, linear
OVERWRITE_POLICIES = ("skip", "replace", "fail")


@dataclass(frozen=True)
class Config:
    target_rate: int
    target_lufs: float
    jobs: int = 1
    overwrite: str = "skip"


def discover(input_dir: Path, output_dir: Path):
    """Recursively find WAV inputs; dedupe sources, flag target conflicts."""
    sources = sorted({p.resolve() for p in input_dir.rglob("*")
                      if p.is_file() and p.suffix.lower() == ".wav"})
    tasks: list[tuple[Path, Path]] = []
    conflicts: list[dict] = []
    seen_targets: dict[Path, Path] = {}
    for src in sources:
        rel = src.relative_to(input_dir.resolve())
        target = (output_dir / rel).resolve()
        if target in seen_targets:
            conflicts.append(report.file_record(
                source=str(src), output=str(target), status="failed",
                error=E_CONFLICT,
                message=f"output path already claimed by {seen_targets[target]}"))
            continue
        seen_targets[target] = src
        tasks.append((src, target))
    return tasks, conflicts


def process_one(source: Path, target: Path, cfg: Config) -> dict:
    """Process one file; never raises — failures become failed records."""
    try:
        if target.exists():
            if cfg.overwrite == "skip":
                return report.file_record(source=str(source), output=str(target),
                                          status="skipped",
                                          message="target exists, policy=skip")
            if cfg.overwrite == "fail":
                return report.file_record(source=str(source), output=str(target),
                                          status="failed", error=E_EXISTS,
                                          message="target exists, policy=fail")
        data, rate_in = read_wav(source)
        frames_in, channels = data.shape

        rms_ch_before, rms_before = loudness.rms_db(data)
        lufs_before = loudness.lufs(data, rate_in)
        clipped_before = loudness.count_clipped(data)
        silent = lufs_before is None

        converted = resample(data, rate_in, cfg.target_rate)

        gain_db = loudness.gain_db_for_target(lufs_before, cfg.target_lufs)
        gain_db, limited = loudness.limit_gain_for_peak(
            converted, cfg.target_rate, gain_db, TRUE_PEAK_CEILING,
            estimate_true_peak)
        gained = converted * (10.0 ** (gain_db / 20.0))

        rms_ch_after, rms_after = loudness.rms_db(gained)
        lufs_after = loudness.lufs(gained, cfg.target_rate)
        clipped_after = loudness.count_clipped(gained)

        write_wav_atomic(target, gained, cfg.target_rate)

        stats = {
            "input_rate": rate_in,
            "output_rate": cfg.target_rate,
            "channels": channels,
            "frames_in": frames_in,
            "frames_out": output_length(frames_in, rate_in, cfg.target_rate),
            "rms_before": report.num(rms_before),
            "rms_after": report.num(rms_after),
            "rms_before_per_channel": [report.num(v) for v in rms_ch_before],
            "rms_after_per_channel": [report.num(v) for v in rms_ch_after],
            "lufs_before": report.num(lufs_before),
            "lufs_after": report.num(lufs_after),
            "clipped_before": clipped_before,
            "clipped_after": clipped_after,
            "gain_db": report.num(gain_db),
            "gain_limited": limited,
            "silent": silent,
        }
        return report.file_record(source=str(source), output=str(target),
                                  status="ok", stats=stats)
    except AudioNormError as exc:
        return report.file_record(source=str(source), output=str(target),
                                  status="failed", error=exc.code,
                                  message=exc.message)
    except Exception as exc:  # defensive: one bad file must not kill the batch
        return report.file_record(source=str(source), output=str(target),
                                  status="failed", error="E_INTERNAL",
                                  message=str(exc))


def run_batch(input_dir: Path, output_dir: Path, cfg: Config) -> dict:
    """Run the whole batch and return the report dictionary."""
    tasks, conflicts = discover(input_dir, output_dir)
    if cfg.jobs > 1 and len(tasks) > 1:
        with ThreadPoolExecutor(max_workers=cfg.jobs) as pool:
            records = list(pool.map(lambda t: process_one(*t, cfg), tasks))
    else:
        records = [process_one(src, dst, cfg) for src, dst in tasks]
    return report.batch_report(
        conflicts + records,
        input_dir=str(input_dir), output_dir=str(output_dir),
        target_rate=cfg.target_rate, target_lufs=cfg.target_lufs)
