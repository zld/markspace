"""Batch scanning and scheduling.

One :class:`~audiocut.detector.SilenceDetector` instance drives every file so
interactive ``analyze`` and on-disk ``export`` provably use the same
algorithm.  Each input is processed independently: a corrupt or failing file
is recorded as an error and never aborts the remaining files.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

from .detector import DetectionResult, DetectorConfig, SilenceDetector
from .exporter import ExportError, export_segments, write_manifest
from .store import ResultStore, hash_file
from .wavio import WavFormatError, WavReader

WAV_SUFFIXES = (".wav", ".wave")


@dataclass
class FileOutcome:
    path: str
    status: str  # "ok", "skipped", or "error"
    result: DetectionResult | None = None
    manifest_path: str | None = None
    exported_paths: list[str] = field(default_factory=list)
    error: str | None = None
    reused_cache: bool = False


@dataclass
class BatchReport:
    outcomes: list[FileOutcome]

    @property
    def has_errors(self) -> bool:
        return any(o.status == "error" for o in self.outcomes)

    @property
    def segment_count(self) -> int:
        return sum(len(o.result.segments) for o in self.outcomes if o.result)


def collect_inputs(paths: list[str]) -> list[Path]:
    """Expand directories to WAV files while keeping explicit file paths."""

    collected: list[Path] = []
    for raw in paths:
        path = Path(raw)
        if path.is_dir():
            collected.extend(
                sorted(p for p in path.rglob("*") if p.suffix.lower() in WAV_SUFFIXES)
            )
        else:
            collected.append(path)
    return collected


def _summary_dict(result: DetectionResult) -> dict:
    return {
        "path": result.path,
        "sample_rate": result.sample_rate,
        "n_frames_total": result.n_frames_total,
        "segments": [
            {"index": s.index, "start_sample": s.start_sample, "end_sample": s.end_sample}
            for s in result.segments
        ],
        "silences": [
            {"start_sample": r.start_sample, "end_sample": r.end_sample}
            for r in result.silences
        ],
        "silence_total_seconds": round(result.silence_total_seconds, 6),
        "noise_floor": result.noise_floor,
        "low_threshold": result.low_threshold,
        "high_threshold": result.high_threshold,
    }


def outcome_to_dict(outcome: FileOutcome) -> dict:
    if outcome.status == "error":
        return {"path": outcome.path, "status": "error", "error": outcome.error}
    payload = {"path": outcome.path, "status": outcome.status,
               "reused_cache": outcome.reused_cache}
    if outcome.result is not None:
        payload.update(_summary_dict(outcome.result))
    if outcome.manifest_path is not None:
        payload["manifest"] = outcome.manifest_path
        payload["exported_files"] = outcome.exported_paths
    return payload


def format_text(report: BatchReport) -> str:
    lines: list[str] = []
    for outcome in report.outcomes:
        if outcome.status == "error":
            lines.append(f"ERROR {outcome.path}: {outcome.error}")
            continue
        result = outcome.result
        tag = " [cached]" if outcome.reused_cache else ""
        lines.append(
            f"{outcome.path}{tag}: segments={len(result.segments)} "
            f"silence_total_seconds={result.silence_total_seconds:.2f}"
        )
        for segment in result.segments:
            lines.append(
                f"  segment {segment.index}: start_sample={segment.start_sample} "
                f"end_sample={segment.end_sample}"
            )
        if outcome.manifest_path:
            lines.append(f"  manifest={outcome.manifest_path}")
    total = report.segment_count
    errors = sum(1 for o in report.outcomes if o.status == "error")
    lines.append(f"summary: files={len(report.outcomes)} segments={total} errors={errors}")
    return "\n".join(lines)


def run_batch(
    inputs: list[str],
    config: DetectorConfig | None = None,
    db_path: str | None = None,
    outdir: str | None = None,
) -> BatchReport:
    """Detect (and optionally export) every input, isolating per-file failures."""

    detector = SilenceDetector(config)  # one detector shared by analyze and export
    store = ResultStore(db_path) if db_path else None
    outcomes: list[FileOutcome] = []
    try:
        for path in collect_inputs(inputs):
            outcome = _process_one(path, detector, store, outdir)
            outcomes.append(outcome)
    finally:
        if store is not None:
            store.close()
    return BatchReport(outcomes)


def _process_one(path: Path, detector: SilenceDetector,
                 store: ResultStore | None, outdir: str | None) -> FileOutcome:
    try:
        input_hash = hash_file(path)
        fingerprint = detector.config.fingerprint()
        result: DetectionResult | None = None
        reused = False
        channels = sample_width = None

        if store is not None:
            file_id = store.find_run(input_hash, fingerprint)
            if file_id is not None:
                with WavReader(path) as reader:
                    channels, sample_width = reader.channels, reader.sample_width
                result = store.load_result(file_id, str(path), detector.config)
                reused = True

        if result is None:
            with WavReader(path) as reader:
                channels, sample_width = reader.channels, reader.sample_width
                result = detector.analyze(reader)
            if store is not None:
                store.save_run(
                    result, detector.config, input_hash,
                    channels, sample_width, str(path),
                )

        manifest_path = None
        exported_paths: list[str] = []
        if outdir is not None:
            exported = export_segments(path, result, outdir)
            exported_paths = [item.path for item in exported]
            manifest_path = write_manifest(
                Path(outdir) / f"{path.stem}_manifest.json",
                outdir, result, exported, path,
            )
        return FileOutcome(
            path=str(path), status="ok", result=result,
            manifest_path=manifest_path, exported_paths=exported_paths,
            reused_cache=reused,
        )
    except (WavFormatError, ExportError, OSError, ValueError, RuntimeError) as exc:
        return FileOutcome(path=str(path), status="error", error=str(exc))
