"""Batch scheduling: path collection, dedup and failure-tolerant processing.

The detector itself stays CLI/database-free; this module is the only place
that combines the WAV reader, calibration, detector and SQLite store.  A
single bad file never aborts the run — it is recorded as a failure while the
remaining files continue — and database problems surface separately so the
CLI can choose exit code 3.
"""

from __future__ import annotations

import os
from dataclasses import dataclass

from .detector import DetectorConfig, DetectionResult, detect_from_blocks
from .errors import AudioSliceError, InputFileError, StoreError
from .store import FileFingerprint, Store, fingerprint, make_cache_key
from .wav import WavReader

WAV_EXTENSIONS = frozenset({".wav", ".wave"})


@dataclass(frozen=True)
class FileOutcome:
    path: str
    status: str  # "ok" | "cached" | "failed" | "skipped"
    reason: str = ""
    result: DetectionResult | None = None
    cache_key: str = ""

    def as_dict(self) -> dict[str, object]:
        data = {"path": self.path, "status": self.status}
        if self.reason:
            data["reason"] = self.reason
        if self.result is not None:
            data.update(self.result.as_dict())
        return data


@dataclass(frozen=True)
class BatchSummary:
    outcomes: tuple[FileOutcome, ...]

    @property
    def files_ok(self) -> int:
        return sum(1 for o in self.outcomes if o.status in ("ok", "cached"))

    @property
    def files_failed(self) -> int:
        return sum(1 for o in self.outcomes if o.status == "failed")

    @property
    def files_skipped(self) -> int:
        return sum(1 for o in self.outcomes if o.status == "skipped")

    @property
    def total_segment_count(self) -> int:
        return sum(len(o.result.segments) for o in self.outcomes if o.result is not None)

    @property
    def total_silence_seconds(self) -> float:
        return round(
            sum(o.result.silence_total_seconds for o in self.outcomes if o.result is not None), 6
        )

    def as_dict(self) -> dict[str, object]:
        return {
            "files_ok": self.files_ok,
            "files_failed": self.files_failed,
            "files_skipped": self.files_skipped,
            "segment_count": self.total_segment_count,
            "silence_total_seconds": self.total_silence_seconds,
            "results": [o.as_dict() for o in self.outcomes],
        }


def collect_wav_paths(inputs: list[str], recursive: bool = True) -> list[str]:
    """Expand files/directories to normalised, de-duplicated WAV paths.

    Paths are made absolute and sorted for deterministic runs.  A missing
    input raises :class:`InputFileError` naming the offending path.
    """
    ordered: list[str] = []
    seen: set[str] = set()

    def add(path: str) -> None:
        norm = os.path.abspath(path)
        if norm not in seen:
            seen.add(norm)
            ordered.append(norm)

    for entry in inputs:
        if os.path.isdir(entry):
            if not recursive:
                raise InputFileError(entry, "path is a directory; recursive scanning is disabled")
            for root, dirs, files in os.walk(entry):
                dirs.sort()
                for name in sorted(files):
                    if os.path.splitext(name)[1].lower() in WAV_EXTENSIONS:
                        add(os.path.join(root, name))
        elif os.path.exists(entry):
            add(entry)
        else:
            raise InputFileError(entry, "input path does not exist")
    return sorted(ordered)


def _config_for_file(info, cfg: DetectorConfig) -> DetectorConfig:
    if info.sample_rate == cfg.sample_rate:
        return cfg
    return DetectorConfig(
        sample_rate=info.sample_rate,
        window_ms=cfg.window_ms,
        noise_margin_db=cfg.noise_margin_db,
        hysteresis_db=cfg.hysteresis_db,
        min_silence_ms=cfg.min_silence_ms,
        min_segment_ms=cfg.min_segment_ms,
        max_pop_gap_ms=cfg.max_pop_gap_ms,
        block_frames=cfg.block_frames,
    )


def _detect_file(path: str, cfg: DetectorConfig) -> tuple[DetectionResult, DetectorConfig]:
    with WavReader(path) as reader:
        effective = _config_for_file(reader.info, cfg)

        def factory():
            reader.rewind()
            while reader.frames_remaining:
                yield reader.read_frames(effective.block_frames)

        return detect_from_blocks(factory, effective), effective


def run_batch(paths: list[str], cfg: DetectorConfig, store: Store | None = None) -> BatchSummary:
    """Process every path once; per-file failures are isolated and recorded."""
    outcomes: list[FileOutcome] = []
    processed: set[str] = set()
    for path in paths:
        if path in processed:
            outcomes.append(FileOutcome(path, "skipped", "duplicate input path"))
            continue
        processed.add(path)
        try:
            fp: FileFingerprint | None = None
            cache_key = ""
            effective_cfg = cfg
            if store is not None:
                fp = fingerprint(path)
                # The key must reflect the parameters actually applied, which
                # include the file's own sample rate.
                with WavReader(path) as probe:
                    effective_cfg = _config_for_file(probe.info, cfg)
                cache_key = make_cache_key(fp, effective_cfg.param_signature())
                if store.has_result(cache_key):
                    cached, _ = _detect_file(path, cfg)
                    outcomes.append(FileOutcome(path, "cached", result=cached, cache_key=cache_key))
                    continue
            result, effective_cfg = _detect_file(path, cfg)
            if store is not None:
                assert fp is not None
                store.save_result(cache_key, fp, effective_cfg.param_signature(), result)
            outcomes.append(FileOutcome(path, "ok", result=result, cache_key=cache_key))
        except StoreError:
            raise
        except AudioSliceError as exc:
            outcomes.append(FileOutcome(path, "failed", exc.reason))
        except Exception as exc:  # defensive: never let one file kill the batch
            outcomes.append(FileOutcome(path, "failed", f"unexpected error: {exc}"))
    return BatchSummary(tuple(outcomes))
