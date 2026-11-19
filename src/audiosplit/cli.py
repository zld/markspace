"""Command-line entry point for batch scanning and JSON Lines export."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any, Sequence

from . import __version__
from .detector import DetectorConfig, FrameEnergyDetector
from .merge import MergeConfig, MergeError, Segment, build_segments, measure
from .store import JobMeta, Store, StoreError, dumps_params
from .wav import WavError, WavInfo, WavReader

_WAV_SUFFIXES = {".wav", ".wave"}
_BLOCK_FRAMES = 16384


class UsageError(ValueError):
    """Bad command line input; maps to exit status 2."""


@dataclass(frozen=True)
class ScanSettings:
    frame_ms: float = 10.0
    on_db: float = 4.0
    off_db: float = 2.0
    min_silence_ms: float = 120.0
    min_event_ms: float = 30.0
    pre_roll: int = 0
    post_roll: int = 0
    noise_window_frames: int = 300
    noise_percentile: float = 20.0
    noise_adapt_rate: float = 0.2

    def detector(self) -> DetectorConfig:
        return DetectorConfig(
            frame_ms=self.frame_ms,
            on_db=self.on_db,
            off_db=self.off_db,
            min_silence_ms=self.min_silence_ms,
            noise_window_frames=self.noise_window_frames,
            noise_percentile=self.noise_percentile,
            noise_adapt_rate=self.noise_adapt_rate,
        )

    def merge(self) -> MergeConfig:
        return MergeConfig(
            min_silence_ms=self.min_silence_ms,
            min_event_ms=self.min_event_ms,
            pre_roll=self.pre_roll,
            post_roll=self.post_roll,
        )

    def validate(self, sample_rate: int | None = None) -> None:
        try:
            self.detector().validate(sample_rate or 48000)
        except ValueError as exc:
            raise UsageError(str(exc)) from exc
        if self.pre_roll < 0 or self.post_roll < 0:
            raise UsageError("pre_roll and post_roll must be non-negative")

    def as_dict(self) -> dict[str, Any]:
        return asdict(self)


@dataclass
class FileOutcome:
    path: str
    ok: bool
    segments: list[Segment]
    error: str | None = None
    segment_count: int = 0
    reused: bool = False


def collect_inputs(target: Path, recursive: bool) -> list[Path]:
    if not target.exists():
        raise UsageError(f"input path does not exist: {target}")
    if target.is_file():
        return [target]
    if not target.is_dir():
        raise UsageError(f"input path is not a file or directory: {target}")
    pattern = "**/*" if recursive else "*"
    files = sorted(
        path
        for path in target.glob(pattern)
        if path.is_file() and path.suffix.lower() in _WAV_SUFFIXES
    )
    return files


def hash_file(path: Path, chunk: int = 1 << 20) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for block in iter(lambda: handle.read(chunk), b""):
            digest.update(block)
    return digest.hexdigest()


def _display_path(path: Path, root: Path | None) -> str:
    if root is None:
        return str(path)
    try:
        return str(path.resolve().relative_to(root.resolve()))
    except ValueError:
        return str(path)


def scan_file(
    path: Path,
    settings: ScanSettings,
) -> tuple[WavInfo, list[Segment], float]:
    reader = WavReader(path)
    info = reader.info
    detector = FrameEnergyDetector(info.sample_rate, settings.detector())
    total = 0
    for block in reader.iter_blocks(_BLOCK_FRAMES):
        detector.push(block)
        total += block.shape[0]
    result = detector.finish(total)
    try:
        raw = build_segments(
            result.events,
            sample_rate=info.sample_rate,
            total_samples=info.total_samples,
            config=settings.merge(),
        )
    except MergeError as exc:
        raise WavError(info.path, str(exc)) from exc
    segments = measure(info.path, info, raw)
    return info, segments, result.noise_floor_db


def _process_file(
    path: Path,
    root: Path | None,
    settings: ScanSettings,
    store: Store,
    run_id: int,
    *,
    force: bool,
    params_json: str,
) -> FileOutcome:
    display = _display_path(path, root)
    try:
        info = WavReader(path).info
        file_hash = hash_file(path)
        if not force:
            reusable = store.find_reusable(display, file_hash, params_json)
            if reusable is not None:
                cloned = store.clone_job(reusable, run_id)
                segments = store.load_segments(cloned.job_id)
                return FileOutcome(
                    path=display,
                    ok=True,
                    segments=segments,
                    segment_count=len(segments),
                    reused=True,
                )
        info, segments, noise_floor_db = scan_file(path, settings)
        store.record_success(
            run_id,
            display,
            file_hash,
            JobMeta(
                sample_rate=info.sample_rate,
                channels=info.channels,
                total_samples=info.total_samples,
                noise_floor_db=noise_floor_db,
            ),
            segments,
        )
        return FileOutcome(
            path=display, ok=True, segments=segments, segment_count=len(segments)
        )
    except (WavError, MergeError, OSError) as exc:
        reason = str(exc)
        try:
            store.record_failure(run_id, display, _safe_hash(path), reason)
        except StoreError as store_exc:
            return FileOutcome(display, False, [], f"{reason}; {store_exc}")
        return FileOutcome(display, False, [], reason)
    except StoreError as exc:
        return FileOutcome(display, False, [], str(exc))


def _safe_hash(path: Path) -> str:
    try:
        return hash_file(path)
    except OSError:
        return "unavailable"


def _write_export(path: Path, rows: list[dict[str, Any]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    try:
        with tmp.open("w", encoding="utf-8") as handle:
            for row in rows:
                handle.write(json.dumps(row, sort_keys=True) + "\n")
        os.replace(tmp, path)
    except OSError as exc:
        try:
            tmp.unlink()
        except OSError:
            pass
        raise UsageError(f"cannot write export {path}: {exc}") from exc


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="audiosplit",
        description="Split long PCM WAV recordings into sample-accurate events.",
    )
    parser.add_argument("--version", action="version", version=__version__)
    sub = parser.add_subparsers(dest="command", required=True)

    scan = sub.add_parser("scan", help="scan WAV files and store segments")
    scan.add_argument("input", type=Path, help="WAV file or directory of WAV files")
    scan.add_argument("--db", type=Path, required=True, help="SQLite database path")
    scan.add_argument("--frame-ms", type=float, default=10.0)
    scan.add_argument("--on-db", type=float, default=4.0)
    scan.add_argument("--off-db", type=float, default=2.0)
    scan.add_argument("--min-silence-ms", type=float, default=120.0)
    scan.add_argument("--min-event-ms", type=float, default=30.0)
    scan.add_argument("--pre-roll", type=int, default=0)
    scan.add_argument("--post-roll", type=int, default=0)
    scan.add_argument("--noise-window-frames", type=int, default=300)
    scan.add_argument("--noise-percentile", type=float, default=20.0)
    scan.add_argument("--noise-adapt-rate", type=float, default=0.2)
    scan.add_argument("--recursive", action="store_true")
    scan.add_argument("--force", action="store_true", help="recompute known files")
    scan.add_argument("--export", type=Path, help="write segments as JSON Lines")
    scan.add_argument("--quiet", action="store_true")

    export = sub.add_parser("export", help="export a stored run as JSON Lines")
    export.add_argument("--db", type=Path, required=True)
    export.add_argument("--run-id", type=int)
    export.add_argument("--out", type=Path, required=True)
    return parser


def _settings_from_args(args: argparse.Namespace) -> ScanSettings:
    return ScanSettings(
        frame_ms=args.frame_ms,
        on_db=args.on_db,
        off_db=args.off_db,
        min_silence_ms=args.min_silence_ms,
        min_event_ms=args.min_event_ms,
        pre_roll=args.pre_roll,
        post_roll=args.post_roll,
        noise_window_frames=args.noise_window_frames,
        noise_percentile=args.noise_percentile,
        noise_adapt_rate=args.noise_adapt_rate,
    )


def _run_scan(args: argparse.Namespace) -> int:
    settings = _settings_from_args(args)
    settings.validate()
    inputs = collect_inputs(args.input, args.recursive)
    root = args.input if args.input.is_dir() else None
    params_json = dumps_params(settings.as_dict())
    try:
        store = Store(args.db)
    except StoreError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    with store:
        run_id = store.start_run(settings.as_dict())
        outcomes: list[FileOutcome] = []
        for path in inputs:
            outcome = _process_file(
                path,
                root,
                settings,
                store,
                run_id,
                force=args.force,
                params_json=params_json,
            )
            outcomes.append(outcome)
            if not args.quiet:
                state = "ok" if outcome.ok else "failed"
                suffix = " (reused)" if outcome.reused else ""
                detail = "" if outcome.ok else f" reason={outcome.error}"
                print(
                    f"{outcome.path}: {state}{suffix} "
                    f"segments={outcome.segment_count}{detail}",
                    file=sys.stderr,
                )
        if args.export is not None:
            try:
                _write_export(args.export, store.export_run(run_id))
            except UsageError as exc:
                print(f"error: {exc}", file=sys.stderr)
                return 2
        silence_total_ms = store.silence_ms(run_id)
    files_ok = sum(1 for outcome in outcomes if outcome.ok)
    files_failed = len(outcomes) - files_ok
    segments = sum(outcome.segment_count for outcome in outcomes)
    print(f"run_id={run_id}")
    print(f"files_ok={files_ok}")
    print(f"files_failed={files_failed}")
    print(f"segments={segments}")
    print(f"silence_total_ms={int(round(silence_total_ms))}")
    return 0 if files_failed == 0 else 1


def _run_export(args: argparse.Namespace) -> int:
    try:
        store = Store(args.db)
    except StoreError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    with store:
        run_id = args.run_id if args.run_id is not None else store.latest_run_id()
        if run_id is None:
            print("error: no runs found in database", file=sys.stderr)
            return 2
        meta = store.get_run(run_id)
        if meta is None:
            print(f"error: run {run_id} not found", file=sys.stderr)
            return 2
        rows = store.export_run(run_id)
        try:
            _write_export(args.out, rows)
        except UsageError as exc:
            print(f"error: {exc}", file=sys.stderr)
            return 2
    print(f"run_id={run_id}")
    print(f"segments={len(rows)}")
    print(f"out={args.out}")
    return 0


def main(argv: Sequence[str] | None = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "scan":
            return _run_scan(args)
        if args.command == "export":
            return _run_export(args)
    except UsageError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    except StoreError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    return 2


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
