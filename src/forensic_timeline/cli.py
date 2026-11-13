"""Command-line entry point: parse arguments and orchestrate the pipeline.

Exit codes:
    0  analysis completed (integrity findings are data, not failures)
    1  unexpected internal error
    2  command-line / configuration error
    3  image or manifest unreadable, invalid, or not a self-generated sample
    4  database locked
    5  disk full / output write failure
"""

from __future__ import annotations

import argparse
import errno
import sys
import tempfile
from datetime import datetime, timedelta, timezone
from pathlib import Path

from . import clock, integrity, reader, report, samplegen, store, timeline
from .config import Config, ConfigError, build_config
from .models import Anomaly, Confidence, Event, IntegrityStatus

EXIT_OK = 0
EXIT_INTERNAL = 1
EXIT_CONFIG = 2
EXIT_IMAGE = 3
EXIT_DB = 4
EXIT_DISK = 5

STEP_NAMES = ("generate", "read", "clock", "integrity", "timeline", "store")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="forensic_timeline",
        description="Rebuild an event timeline from a self-generated local "
                    "sample image. Real disks, partitions and external "
                    "evidence sources are never touched.",
    )
    parser.add_argument("--self-check", action="store_true",
                        help="generate a fresh sample image in a temporary "
                             "directory and analyse it end to end")
    parser.add_argument("--image", help="path to a sample image file")
    parser.add_argument("--manifest", help="path to the sample manifest JSON")
    parser.add_argument("--timezone",
                        help="IANA timezone for naive record timestamps "
                             "(e.g. Asia/Shanghai); required")
    parser.add_argument("--drift-limit-seconds", type=int, default=300,
                        help="maximum clock drift that will be corrected "
                             "(default 300)")
    parser.add_argument("--gap-threshold-seconds", type=int, default=900,
                        help="intervals longer than this are reported as "
                             "missing (default 900)")
    parser.add_argument("--db", help="SQLite database path for run batches")
    parser.add_argument("--json", help="also write a machine-readable JSON "
                                       "report to this path")
    parser.add_argument("--max-blocks", type=int, default=1_000_000)
    parser.add_argument("--max-image-bytes", type=int, default=1 << 30)
    parser.add_argument("--max-records", type=int, default=1_000_000)
    return parser


def _steps_line(steps: dict[str, str]) -> str:
    return "steps: " + " ".join(f"{name}={state}" for name, state in steps.items())


def _fail(code: int, category: str, message: str, steps: dict[str, str]) -> int:
    print(f"error={category}: {message}", file=sys.stderr)
    print(_steps_line(steps), file=sys.stderr)
    return code


def _os_error(exc: OSError, steps: dict[str, str]) -> int:
    if exc.errno == errno.ENOSPC:
        return _fail(EXIT_DISK, "disk_full", str(exc), steps)
    return _fail(EXIT_IMAGE, "io", str(exc), steps)


def _utc_iso(dt: datetime) -> str:
    return dt.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        cfg = build_config(args)
    except ConfigError as exc:
        print(f"error=config: {exc}", file=sys.stderr)
        return EXIT_CONFIG

    steps = {name: "not_run" for name in STEP_NAMES}

    # --- stage: sample acquisition (self-check only) ----------------------
    if args.self_check:
        workdir = Path(tempfile.mkdtemp(prefix="forensic-timeline-"))
        try:
            spec = samplegen.generate_sample(workdir)
        except (OSError, ValueError) as exc:
            if isinstance(exc, OSError):
                return _os_error(exc, steps)
            return _fail(EXIT_INTERNAL, "samplegen", str(exc), steps)
        image_path, manifest_path = spec.image_path, spec.manifest_path
        steps["generate"] = "ok"
        print(f"sample_dir={workdir}")
    else:
        if not args.image or not args.manifest:
            return _fail(EXIT_CONFIG, "config",
                         "--image and --manifest are required without --self-check",
                         steps)
        image_path, manifest_path = Path(args.image), Path(args.manifest)
        steps["generate"] = "skipped"

    # --- stage: read ------------------------------------------------------
    try:
        manifest = reader.load_manifest(manifest_path)
        read = reader.read_image(
            image_path,
            block_size=manifest["block_size"],
            max_blocks=cfg.max_blocks,
            max_image_bytes=cfg.max_image_bytes,
            max_records=cfg.max_records,
        )
    except reader.ImageReadError as exc:
        return _fail(EXIT_IMAGE, "image", str(exc), steps)
    steps["read"] = "ok"
    anomalies: list[Anomaly] = list(read.anomalies)

    # --- stage: clock normalization and drift correction ------------------
    events: list[Event] = []
    for rec in read.records:
        parsed = clock.parse_record_time(rec.raw_timestamp, cfg.timezone,
                                         cfg.timezone_name)
        if parsed.anomaly is not None or parsed.utc is None:
            anomalies.append(Anomaly(
                parsed.anomaly or "unparseable_time",
                f"event {rec.event_id}: timestamp {rec.raw_timestamp!r} isolated",
                rec.source_block, rec.source_offset))
            continue
        events.append(Event(
            event_id=rec.event_id,
            raw_timestamp=rec.raw_timestamp,
            raw_timezone=parsed.tz_label,
            naive=parsed.naive,
            utc_raw=parsed.utc,
            utc_final=parsed.utc,
            corrected=False,
            confidence=Confidence.LOW,
            event_type=rec.event_type,
            description=rec.description,
            source_block=rec.source_block,
            source_offset=rec.source_offset,
            seq=rec.seq,
        ))

    estimate = clock.estimate_drift(
        {e.event_id: e.utc_raw for e in events},
        manifest.get("anchors", []),
        cfg.drift_limit_seconds,
        cfg.min_anchors,
    )
    if estimate.applied:
        for event in events:
            event.utc_final = event.utc_raw - timedelta(seconds=estimate.drift_seconds)
            event.corrected = True
            event.confidence = Confidence.HIGH
    steps["clock"] = "ok"

    # --- stage: integrity -------------------------------------------------
    integrity_results = integrity.verify_blocks(read.blocks, manifest.get("blocks", []))
    whole_status = integrity.whole_image_status(read.whole_sha256, manifest)
    steps["integrity"] = "ok"

    # --- stage: timeline --------------------------------------------------
    try:
        acq_start = datetime.fromisoformat(manifest["acquisition_start_utc"])
        acq_end = datetime.fromisoformat(manifest["acquisition_end_utc"])
    except ValueError as exc:
        return _fail(EXIT_IMAGE, "manifest",
                     f"invalid acquisition boundary timestamps: {exc}", steps)
    ordered = timeline.sort_and_flag(events)
    gaps = timeline.find_gaps(ordered, acq_start, acq_end, cfg.gap_threshold_seconds)
    steps["timeline"] = "ok"

    # --- stage: store -----------------------------------------------------
    if cfg.db_path is not None:
        db_path = cfg.db_path
    elif args.self_check:
        db_path = workdir / "forensic-timeline.sqlite"
    else:
        db_path = Path(tempfile.gettempdir()) / "forensic-timeline.sqlite"

    batch = store.BatchData(
        batch_id=store.new_batch_id(),
        started_utc=store.utc_now_iso(),
        status="completed",
        config={
            "timezone": cfg.timezone_name,
            "drift_limit_seconds": cfg.drift_limit_seconds,
            "gap_threshold_seconds": cfg.gap_threshold_seconds,
            "max_blocks": cfg.max_blocks,
            "max_image_bytes": cfg.max_image_bytes,
            "max_records": cfg.max_records,
        },
        inputs={
            "image": str(image_path),
            "manifest": str(manifest_path),
            "image_size": read.size,
            "image_sha256": read.whole_sha256,
            "self_check": bool(args.self_check),
        },
        events=ordered,
        anomalies=anomalies,
        integrity=integrity_results,
        gaps=gaps,
    )
    try:
        conn = store.connect(db_path)
        try:
            store.save_batch(conn, batch)
        finally:
            conn.close()
    except store.DatabaseLockedError as exc:
        return _fail(EXIT_DB, "db_locked", str(exc), steps)
    except store.DiskFullError as exc:
        return _fail(EXIT_DISK, "disk_full", str(exc), steps)
    except store.StoreError as exc:
        return _fail(EXIT_DB, "db", str(exc), steps)
    steps["store"] = "ok"

    # --- stage: report ----------------------------------------------------
    def count_status(status: IntegrityStatus) -> int:
        return sum(1 for r in integrity_results if r.status is status)

    rejected = sum(1 for a in anomalies
                   if a.kind in ("rejected_record", "undecodable_record"))
    time_anomalies = sum(1 for a in anomalies
                         if a.kind in ("ambiguous_local_time",
                                       "nonexistent_local_time",
                                       "unparseable_time"))
    summary = {
        "batch_id": batch.batch_id,
        "image": str(image_path),
        "manifest": str(manifest_path),
        "timezone": cfg.timezone_name,
        "events": len(ordered),
        "duplicates": sum(1 for e in ordered if e.duplicate_of is not None),
        "rejected_records": rejected,
        "time_anomalies": time_anomalies,
        "hash_mismatches": count_status(IntegrityStatus.HASH_MISMATCH),
        "missing_blocks": count_status(IntegrityStatus.MISSING_BLOCK),
        "extra_blocks": count_status(IntegrityStatus.EXTRA_BLOCK),
        "bad_hash_entries": count_status(IntegrityStatus.BAD_HASH_FORMAT),
        "whole_image": whole_status,
        "missing_intervals": len(gaps),
        "clock_drift_seconds": estimate.drift_seconds,
        "clock_correction": "applied" if estimate.applied else "skipped",
        "clock_confidence": estimate.confidence.value,
        "clock_reason": estimate.reason,
        "db": str(db_path),
        "gaps": [
            {"kind": g.kind, "start_utc": _utc_iso(g.start_utc),
             "end_utc": _utc_iso(g.end_utc),
             "duration_seconds": g.duration_seconds}
            for g in gaps
        ],
        "hash_mismatch_details": [
            {"block_index": r.block_index, "offset": r.offset,
             "expected": r.expected, "actual": r.actual}
            for r in integrity_results
            if r.status is IntegrityStatus.HASH_MISMATCH
        ],
        "events_detail": [
            {"event_id": e.event_id, "raw_timestamp": e.raw_timestamp,
             "raw_timezone": e.raw_timezone, "utc_raw": _utc_iso(e.utc_raw),
             "utc_final": _utc_iso(e.utc_final), "corrected": e.corrected,
             "confidence": e.confidence.value, "type": e.event_type,
             "duplicate_of": e.duplicate_of,
             "source_block": e.source_block, "source_offset": e.source_offset}
            for e in ordered
        ],
        "anomalies": [
            {"kind": a.kind, "detail": a.detail,
             "block_index": a.block_index, "offset": a.offset}
            for a in anomalies
        ],
        "steps": steps,
    }
    report.print_summary(summary)

    if cfg.json_path is not None:
        try:
            report.write_json(summary, cfg.json_path)
        except OSError as exc:
            return _os_error(exc, steps)
        print(f"json_report={cfg.json_path}")

    return EXIT_OK


if __name__ == "__main__":
    raise SystemExit(main())
