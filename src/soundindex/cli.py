"""Command line interface: scan / export / query.

Exit codes: 0 = all files ok, 1 = some files failed, 2 = parameter or
global storage/export error.
"""
from __future__ import annotations

import argparse
import hashlib
import os
import sys

from . import audio_io, detector, features
from .exporter import ExportError, export_jsonl
from .models import DetectParams, ParamError
from .store import Store, StoreError

EXIT_OK = 0
EXIT_PARTIAL = 1
EXIT_ERROR = 2


def _params_from(args) -> DetectParams:
    return DetectParams(
        frame_ms=args.frame_ms,
        hop_ms=args.hop_ms,
        noise_margin_db=args.noise_margin_db,
        min_silence_ms=args.min_silence_ms,
        min_clip_ms=args.min_clip_ms,
        fade_guard_ms=args.fade_guard_ms,
    ).validate()


def _hash_file(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for block in iter(lambda: fh.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def _collect_inputs(input_path: str):
    if os.path.isdir(input_path):
        return sorted(
            os.path.join(input_path, name)
            for name in os.listdir(input_path)
            if name.lower().endswith(".wav")
            and os.path.isfile(os.path.join(input_path, name)))
    if os.path.isfile(input_path):
        return [input_path]
    raise ParamError(f"input not found: {input_path}")


def _detect_file(path: str, params: DetectParams):
    info, chunks = audio_io.open_wav(path)
    frame = max(1, params.ms_to_samples(params.frame_ms, info.sample_rate))
    hop = max(1, params.ms_to_samples(params.hop_ms, info.sample_rate))
    env = features.frame_envelope_db(chunks, frame, hop)
    return info, detector.detect(env, info.n_samples, info.sample_rate, params)


def cmd_scan(args) -> int:
    try:
        params = _params_from(args)
        inputs = _collect_inputs(args.input)
        if not inputs:
            raise ParamError(f"no WAV files found in {args.input}")
    except ParamError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_ERROR
    try:
        store = Store(args.db)
    except StoreError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_ERROR

    run_id = store.begin_run(params)
    fp = params.fingerprint()
    ok = failed = reused = clips_total = 0
    silence_total_ms = 0.0
    for path in inputs:
        try:
            content_hash = _hash_file(path)
        except OSError as exc:
            content_hash = ""
            store.record_failure(run_id, path, content_hash, fp, str(exc))
            failed += 1
            print(f"failed: {path}: {exc}", file=sys.stderr)
            continue
        existing = None if args.force else store.lookup_ok(path, content_hash, fp)
        if existing is not None:
            rec = store.file_record(existing)
            ok += 1
            reused += 1
            clips_total += len(rec["clips"])
            silence_total_ms += rec["silence_total_ms"]
            print(f"reused: {path} clips={len(rec['clips'])}")
            continue
        try:
            info, result = _detect_file(path, params)
            store.replace_result(run_id, path, content_hash, params, info, result)
        except (audio_io.AudioError, OSError) as exc:
            store.record_failure(run_id, path, content_hash, fp, str(exc))
            failed += 1
            print(f"failed: {path}: {exc}", file=sys.stderr)
            continue
        except StoreError as exc:
            print(f"error: {exc}", file=sys.stderr)
            store.finish_run(run_id, "failed")
            return EXIT_ERROR
        ok += 1
        clips_total += len(result.clips)
        silence_total_ms += result.silence_total_samples * 1000.0 / info.sample_rate
        print(f"scanned: {path} clips={len(result.clips)}")

    store.finish_run(run_id, "ok" if failed == 0 else "partial")
    if args.export:
        try:
            export_jsonl(store, args.export)
        except ExportError as exc:
            print(f"error: {exc}", file=sys.stderr)
            return EXIT_ERROR
        print(f"exported: {args.export}")
    print(f"files_ok={ok} files_failed={failed} reused={reused} "
          f"clips_total={clips_total} silence_total_ms={silence_total_ms:.1f}")
    return EXIT_OK if failed == 0 else EXIT_PARTIAL


def cmd_export(args) -> int:
    try:
        store = Store(args.db)
        n_files, n_clips = export_jsonl(store, args.out)
    except (StoreError, ExportError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_ERROR
    print(f"exported files={n_files} clips={n_clips} -> {args.out}")
    return EXIT_OK


def cmd_query(args) -> int:
    try:
        store = Store(args.db)
    except StoreError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_ERROR
    rows = store.query_files(args.path)
    for r in rows:
        err = f" error={r['error']}" if r["error"] else ""
        print(f"{r['path']} status={r['status']} clips={r['clip_count']} "
              f"silence_total_ms={(r['silence_total_ms'] or 0.0):.1f}{err}")
    print(f"files={len(rows)}")
    return EXIT_OK


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="soundindex",
        description="Offline WAV silence/clip indexer for game audio assets")
    sub = p.add_subparsers(dest="command", required=True)

    def add_detect(sp):
        sp.add_argument("--frame-ms", type=float, default=20.0)
        sp.add_argument("--hop-ms", type=float, default=10.0)
        sp.add_argument("--noise-margin-db", type=float, default=8.0)
        sp.add_argument("--min-silence-ms", type=float, default=120.0)
        sp.add_argument("--min-clip-ms", type=float, default=80.0)
        sp.add_argument("--fade-guard-ms", type=float, default=30.0)

    s = sub.add_parser("scan", help="scan WAV files, store results, optionally export")
    s.add_argument("--input", required=True, help="WAV file or directory of WAV files")
    s.add_argument("--db", required=True, help="SQLite database path")
    s.add_argument("--export", help="also write a JSONL index to this path")
    s.add_argument("--force", action="store_true",
                   help="re-detect even if a committed result exists")
    add_detect(s)
    s.set_defaults(func=cmd_scan)

    e = sub.add_parser("export", help="export committed results to JSONL")
    e.add_argument("--db", required=True)
    e.add_argument("--out", required=True)
    e.set_defaults(func=cmd_export)

    q = sub.add_parser("query", help="list stored per-file results")
    q.add_argument("--db", required=True)
    q.add_argument("--path", help="restrict to one input path")
    q.set_defaults(func=cmd_query)
    return p


def main(argv=None) -> int:
    args = build_parser().parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
