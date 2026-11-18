"""Batch scheduling: stable directory order, one shared detector, resumable runs.

Only ``*.wav`` files directly inside the given directory are processed (the
tool never reads audio outside it). A single :class:`SilenceDetector` is
configured once and reused for every file. Per-file failures are recorded as
error results while processing continues; already-cached input+parameter
combinations are skipped, so an interrupted batch simply resumes on re-run.
"""
from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path

from .detector import DetectParams, SilenceDetector
from .signal import AudioError, read_wav
from .storage import Storage, file_fingerprint


@dataclass
class FileOutcome:
    path: str
    status: str                      # "ok" | "error" | "cached"
    segments: int = 0
    silence_total_seconds: float = 0.0
    error: str | None = None
    result_id: int | None = None

    def to_dict(self) -> dict:
        out = {"path": self.path, "status": self.status}
        if self.status == "error":
            out["error"] = self.error
        else:
            out["segments"] = self.segments
            out["silence_total_seconds"] = round(self.silence_total_seconds, 6)
            out["result_id"] = self.result_id
        return out


@dataclass
class BatchSummary:
    input_dir: str
    params: DetectParams
    files: list[FileOutcome] = field(default_factory=list)

    @property
    def processed(self) -> int:
        """Files successfully analysed in this run (excludes cached and failed)."""
        return sum(1 for f in self.files if f.status == "ok")

    @property
    def cached(self) -> int:
        return sum(1 for f in self.files if f.status == "cached")

    @property
    def failed(self) -> int:
        return sum(1 for f in self.files if f.status == "error")

    @property
    def segment_total(self) -> int:
        return sum(f.segments for f in self.files)

    def exit_code(self) -> int:
        return 1 if self.failed else 0


def list_wav_files(input_dir: str | Path) -> list[Path]:
    """Stable, non-recursive listing of WAV files inside ``input_dir``."""
    root = Path(input_dir)
    if not root.exists() or not root.is_dir():
        raise FileNotFoundError(f"input directory not found: {root}")
    return sorted(
        (p for p in root.iterdir() if p.is_file() and p.suffix.lower() == ".wav"),
        key=lambda p: p.name,
    )


def process_directory(
    input_dir: str | Path, storage: Storage, params: DetectParams
) -> BatchSummary:
    params.validate()
    detector = SilenceDetector(params)
    param_fp = params.fingerprint()
    params_json = json.dumps(params.to_dict(), sort_keys=True)
    summary = BatchSummary(input_dir=str(Path(input_dir)), params=params)

    for wav_path in list_wav_files(input_dir):
        path_str = str(wav_path)
        sha, size = "", 0
        try:
            sha, size = file_fingerprint(wav_path)
            cached = storage.lookup_result(sha, param_fp)
            if cached is not None:
                summary.files.append(
                    FileOutcome(
                        path=path_str,
                        status="cached",
                        segments=len(cached["segments"]),
                        silence_total_seconds=(
                            int(cached["silence_total_samples"] or 0)
                            / int(cached["task_sample_rate"] or 1)
                        ),
                        result_id=int(cached["id"]),
                    )
                )
                continue
            wave = read_wav(wav_path)
            result = detector.detect(wave.samples, wave.sample_rate).to_dict()
            result_id = storage.save_result(
                path=path_str,
                sha256=sha,
                byte_size=size,
                sample_rate=wave.sample_rate,
                n_samples=wave.n_samples,
                params_json=params_json,
                param_fingerprint=param_fp,
                detection=result,
                error_message=None,
            )
            summary.files.append(
                FileOutcome(
                    path=path_str,
                    status="ok",
                    segments=len(result["segments"]),
                    silence_total_seconds=result["silence_total_seconds"],
                    result_id=result_id,
                )
            )
        except (AudioError, OSError) as exc:
            _record_error(
                storage, summary, path_str, sha, size, params_json, param_fp, exc
            )
    return summary


def _record_error(
    storage: Storage,
    summary: BatchSummary,
    path_str: str,
    sha: str,
    size: int,
    params_json: str,
    param_fp: str,
    exc: Exception,
) -> None:
    message = f"{type(exc).__name__}: {exc}"
    result_id = None
    if sha:
        try:
            result_id = storage.save_result(
                path=path_str,
                sha256=sha,
                byte_size=size,
                sample_rate=0,
                n_samples=0,
                params_json=params_json,
                param_fingerprint=param_fp,
                detection=None,
                error_message=message,
            )
        except Exception:
            result_id = None
    summary.files.append(
        FileOutcome(path=path_str, status="error", error=message, result_id=result_id)
    )
