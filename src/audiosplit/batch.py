"""Batch scheduling: discovery, per-file processing, failure isolation."""

from __future__ import annotations

import os
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Sequence

from .audio_io import AudioError, fingerprint_file, read_wav
from .config import ConfigError, DetectionConfig
from .detector import DetectionError, detect
from .exporter import ExportError, ExportSegment, export_index, export_run
from .segments import SegmentPlan, finalize
from .store import Store


@dataclass(frozen=True)
class InputFile:
    path: Path
    relative_path: str


@dataclass
class FileResult:
    relative_path: str
    status: str
    run_id: int | None = None
    sample_rate: int = 0
    total_samples: int = 0
    silence_samples: int = 0
    segments: tuple[tuple[int, int], ...] = ()
    error: str = ""

    @property
    def seconds(self) -> tuple[float, float]:
        if not self.sample_rate:
            return (0.0, 0.0)
        return (
            self.silence_samples / self.sample_rate,
            self.total_samples / self.sample_rate,
        )


@dataclass
class BatchResult:
    files_ok: int = 0
    files_failed: int = 0
    files_skipped: int = 0
    segments_total: int = 0
    silence_seconds: float = 0.0
    duration_seconds: float = 0.0
    results: list[FileResult] = field(default_factory=list)
    failures: list[tuple[str, str]] = field(default_factory=list)

    @property
    def exit_code(self) -> int:
        return 1 if self.files_failed else 0


def discover_inputs(input_path: Path) -> list[InputFile]:
    """Return regular WAV files sorted by stable relative path.

    Symlinks (files or directories) are ignored and directory symlinks are not
    descended into.
    """
    if not input_path.exists():
        raise ConfigError(f"input does not exist: {input_path}")
    if input_path.is_symlink():
        return []
    if input_path.is_file():
        if input_path.suffix.lower() != ".wav":
            raise ConfigError(f"input is not a .wav file: {input_path}")
        return [InputFile(path=input_path, relative_path=input_path.name)]
    if not input_path.is_dir():
        raise ConfigError(f"input is neither a file nor a directory: {input_path}")

    found: list[InputFile] = []
    for current, dirnames, filenames in os.walk(input_path, followlinks=False):
        dirnames[:] = sorted(
            name for name in dirnames if not (Path(current) / name).is_symlink()
        )
        for name in filenames:
            path = Path(current) / name
            if path.is_symlink() or not path.is_file():
                continue
            if path.suffix.lower() != ".wav":
                continue
            relative = path.relative_to(input_path).as_posix()
            found.append(InputFile(path=path, relative_path=relative))
    found.sort(key=lambda item: item.relative_path)
    return found


def new_batch_id() -> str:
    return uuid.uuid4().hex


def _bundles(plan: SegmentPlan) -> list[ExportSegment]:
    return [
        ExportSegment(
            index=item.index,
            start=item.start,
            end=item.end,
            fade_in=item.fade_in,
            fade_out=item.fade_out,
            peak_dbfs=item.peak_dbfs,
            rms_dbfs=item.rms_dbfs,
        )
        for item in plan.segments
    ]


def _stored_bundles(items: Sequence) -> list[ExportSegment]:
    return [
        ExportSegment(
            index=item.idx,
            start=item.start_sample,
            end=item.end_sample,
            fade_in=item.fade_in_samples,
            fade_out=item.fade_out_samples,
            peak_dbfs=item.peak_dbfs,
            rms_dbfs=item.rms_dbfs,
        )
        for item in items
    ]


def _failed(item: InputFile, exc: Exception) -> FileResult:
    return FileResult(item.relative_path, "failed", error=str(exc))


def process_file(
    item: InputFile,
    *,
    store: Store,
    config: DetectionConfig,
    batch_id: str,
    export_dir: Path | None,
    on_duplicate: str,
    index_entries: list[dict[str, object]],
) -> FileResult:
    if item.path.is_symlink():
        return FileResult(item.relative_path, "skipped", error="symbolic link")
    try:
        fingerprint = fingerprint_file(item.path, config)
        existing = store.find_run(fingerprint, config)
        reuse = existing is not None and on_duplicate == "skip"

        if reuse and export_dir is None:
            segments = store.load_segments(existing.run_id)
            return FileResult(
                relative_path=item.relative_path,
                status="skipped",
                run_id=existing.run_id,
                sample_rate=existing.sample_rate,
                total_samples=existing.total_samples,
                silence_samples=existing.silence_samples,
                segments=tuple((s.start_sample, s.end_sample) for s in segments),
            )

        audio = read_wav(item.path, config)
        if reuse:
            stored = store.load_segments(existing.run_id)
            silences = store.load_silences(existing.run_id)
            bundles = _stored_bundles(stored)
            run_id = existing.run_id
            total = existing.total_samples
            silence_samples = existing.silence_samples
            status = "skipped"
        else:
            detection = detect(audio.samples, audio.sample_rate, config)
            plan = finalize(detection, audio.samples, config)
            record = store.save_run(
                batch_id=batch_id,
                fingerprint=fingerprint,
                config=config,
                input_path=str(item.path),
                relative_path=item.relative_path,
                plan=plan,
            )
            bundles = _bundles(plan)
            silences = plan.silence
            run_id = record.run_id
            total = plan.total_samples
            silence_samples = plan.silence_samples
            status = "ok"

        if export_dir is not None:
            export_run(
                dest_root=export_dir,
                relative_path=item.relative_path,
                samples=audio.samples,
                sample_rate=audio.sample_rate,
                total_samples=total,
                segments=bundles,
                silences=silences,
                config=config,
                run_id=run_id,
                fingerprint=fingerprint,
            )
        index_entries.append(
            {
                "path": item.relative_path,
                "run_id": run_id,
                "segment_count": len(bundles),
                "silence_samples": silence_samples,
            }
        )
        return FileResult(
            relative_path=item.relative_path,
            status=status,
            run_id=run_id,
            sample_rate=audio.sample_rate,
            total_samples=total,
            silence_samples=silence_samples,
            segments=tuple((s.start, s.end) for s in bundles),
        )
    except (AudioError, DetectionError, ExportError, OSError) as exc:
        return _failed(item, exc)


def run_batch(
    input_path: Path,
    *,
    store: Store,
    config: DetectionConfig,
    export_dir: Path | None = None,
    on_duplicate: str = "skip",
) -> BatchResult:
    """Validate everything up front, then process files with isolation."""
    config.validate()
    if on_duplicate not in ("skip", "new"):
        raise ConfigError("on_duplicate must be 'skip' or 'new'")
    inputs = discover_inputs(input_path)
    batch_id = new_batch_id()
    result = BatchResult()
    index_entries: list[dict[str, object]] = []
    for item in inputs:
        file_result = process_file(
            item,
            store=store,
            config=config,
            batch_id=batch_id,
            export_dir=export_dir,
            on_duplicate=on_duplicate,
            index_entries=index_entries,
        )
        result.results.append(file_result)
        if file_result.status == "failed":
            result.files_failed += 1
            result.failures.append((item.relative_path, file_result.error))
            continue
        result.files_ok += 1
        if file_result.status == "skipped":
            result.files_skipped += 1
        result.segments_total += len(file_result.segments)
        silence_seconds, duration_seconds = file_result.seconds
        result.silence_seconds += silence_seconds
        result.duration_seconds += duration_seconds
    if export_dir is not None:
        try:
            export_index(export_dir, index_entries)
        except ExportError as exc:
            result.files_failed += 1
            result.failures.append(("<index>", str(exc)))
    return result
