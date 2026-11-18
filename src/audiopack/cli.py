"""Command line interface: make-fixtures, process, verify."""

from __future__ import annotations

import argparse
import sys
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import numpy as np
import soundfile as sf

from audiopack import decode, discovery, encode, loudness, report
from audiopack.models import (
    BatchSummary,
    FileResult,
    FileStats,
    ProcessConfig,
)
from audiopack.resample import ResamplerKernel, output_length

EXIT_OK = 0
EXIT_PARTIAL = 1
EXIT_BATCH = 2


def process_one(
    src: Path,
    dst: Path,
    relpath: str,
    config: ProcessConfig,
    kernels: dict[tuple[int, int], ResamplerKernel],
) -> FileResult:
    """Decode -> measure -> normalize -> resample -> encode -> verify one file."""
    try:
        meta = discovery.probe_file(src)
        data, meta = decode.decode_file(src, meta)
        rate_in = meta.sample_rate

        rms_before = loudness.rms_dbfs(data, rate_in)
        lufs_before = loudness.integrated_loudness(data, rate_in)
        peak_before = max(
            loudness.sample_peak_dbfs(data),
            loudness.true_peak_dbfs(data, rate_in),
        )
        clipped_before = loudness.count_clipped(data)

        gain_db = loudness.normalization_gain_db(
            lufs_before, config.target_lufs, peak_before, config.peak_ceiling_dbfs
        )
        scaled = data.astype(np.float64) * (10.0 ** (gain_db / 20.0))

        key = (rate_in, config.sample_rate)
        kernel = kernels.get(key)
        if kernel is None:
            kernel = kernels[key] = ResamplerKernel(*key)
        out = kernel.resample(scaled)

        encode.write_wav_atomic(dst, out.astype(np.float32), config.sample_rate)
        meta_out, clipped_after = encode.verify_wav(
            dst, config.sample_rate, meta.channels, out.shape[0]
        )

        stats = FileStats(
            sample_rate_in=rate_in,
            sample_rate_out=meta_out.sample_rate,
            channels=meta.channels,
            frames_in=meta.frames,
            frames_out=meta_out.frames,
            rms_before_dbfs=rms_before,
            rms_after_dbfs=loudness.rms_dbfs(out, config.sample_rate),
            lufs_before=lufs_before,
            lufs_after=loudness.integrated_loudness(out, config.sample_rate),
            gain_db=gain_db,
            peak_before_dbfs=peak_before,
            peak_after_dbfs=loudness.sample_peak_dbfs(out),
            clipped_samples_before=clipped_before,
            clipped_samples_after=clipped_after,
        )
        return FileResult(relpath=relpath, status="ok", stats=stats)
    except Exception as exc:
        try:
            if dst.exists():
                dst.unlink()
        except OSError:
            pass
        return FileResult(relpath=relpath, status="failed", error=str(exc))


def run_process(args: argparse.Namespace) -> int:
    config = ProcessConfig(
        input_dir=Path(args.input),
        output_dir=Path(args.output),
        sample_rate=args.rate,
        target_lufs=args.target_lufs,
        jobs=args.jobs,
        peak_ceiling_dbfs=args.peak_ceiling,
    )
    try:
        input_dir, output_dir = discovery.validate_batch_paths(
            config.input_dir, config.output_dir
        )
        config = ProcessConfig(
            input_dir=input_dir,
            output_dir=output_dir,
            sample_rate=config.sample_rate,
            target_lufs=config.target_lufs,
            jobs=config.jobs,
            peak_ceiling_dbfs=config.peak_ceiling_dbfs,
        )
        inputs = discovery.discover_inputs(input_dir)
        if not inputs:
            raise discovery.DiscoveryError(f"no WAV/FLAC files under {input_dir}")
        plan = discovery.plan_outputs(input_dir, output_dir, inputs)
    except discovery.DiscoveryError as exc:
        print(f"batch error: {exc}", file=sys.stderr)
        return EXIT_BATCH

    kernels: dict[tuple[int, int], ResamplerKernel] = {}
    summary = BatchSummary()
    with ThreadPoolExecutor(max_workers=config.jobs) as pool:
        futures = [
            pool.submit(process_one, src, dst, rel, config, kernels)
            for src, dst, rel in plan
        ]
        for fut in futures:
            result = fut.result()
            summary.results.append(result)
            if result.status == "ok":
                summary.files_ok += 1
            else:
                summary.files_failed += 1
                print(f"failed: {result.relpath}: {result.error}", file=sys.stderr)

    try:
        report.write_report_atomic(
            Path(args.report), report.build_report(config, summary)
        )
    except Exception as exc:
        print(f"batch error: cannot write report: {exc}", file=sys.stderr)
        return EXIT_BATCH

    print(f"files_ok={summary.files_ok} files_failed={summary.files_failed}")
    return EXIT_OK if summary.files_failed == 0 else EXIT_PARTIAL


def run_make_fixtures(args: argparse.Namespace) -> int:
    out_dir = Path(args.dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    rate = 44100
    rng = np.random.default_rng(20240501)

    t1 = np.arange(int(1.5 * rate)) / rate
    env = 0.5 * (1.0 + np.sin(2 * np.pi * 3.0 * t1))
    voice_l = 0.3 * env * np.sin(2 * np.pi * 180 * t1)
    voice_r = 0.3 * env * np.sin(2 * np.pi * 240 * t1)
    sf.write(out_dir / "voice.wav", np.stack([voice_l, voice_r], 1), rate, subtype="PCM_16")

    t2 = np.arange(int(1.0 * rate)) / rate
    tone = 0.5 * np.sin(2 * np.pi * 440 * t2) + 0.2 * np.sin(2 * np.pi * 880 * t2)
    sf.write(out_dir / "tone.wav", np.stack([tone, tone], 1), rate, subtype="PCM_16")

    t3 = np.arange(int(2.0 * rate)) / rate
    noise = rng.standard_normal(t3.shape[0])
    kernel = np.ones(32) / 32.0
    amb = 0.1 * np.convolve(noise, kernel, mode="same")
    sf.write(out_dir / "ambience.wav", np.stack([amb, amb], 1), rate, subtype="PCM_16")

    print(f"fixtures written to {out_dir}: voice.wav tone.wav ambience.wav")
    return EXIT_OK


_REQUIRED_STAT_KEYS = (
    "sample_rate_in", "sample_rate_out", "channels", "frames_in", "frames_out",
    "rms_before_dbfs", "rms_after_dbfs", "lufs_before", "lufs_after", "gain_db",
    "peak_before_dbfs", "peak_after_dbfs",
    "clipped_samples_before", "clipped_samples_after",
)


def run_verify(args: argparse.Namespace) -> int:
    try:
        doc = report.load_report(Path(args.report))
    except Exception as exc:
        print(f"batch error: cannot read report: {exc}", file=sys.stderr)
        return EXIT_BATCH
    problems: list[str] = []
    checked = 0
    target = doc.get("config", {}).get("target_lufs")
    rate_out = doc.get("config", {}).get("sample_rate")
    for entry in doc.get("files", []):
        path = entry.get("path", "?")
        if entry.get("status") != "ok":
            problems.append(f"{path}: status={entry.get('status')}")
            continue
        stats = entry.get("stats") or {}
        missing = [k for k in _REQUIRED_STAT_KEYS if k not in stats]
        if missing:
            problems.append(f"{path}: missing stats {missing}")
            continue
        checked += 1
        if stats["clipped_samples_after"] != 0:
            problems.append(f"{path}: clipped_samples_after={stats['clipped_samples_after']}")
        if stats["sample_rate_out"] != rate_out:
            problems.append(f"{path}: sample_rate_out={stats['sample_rate_out']}")
        expect_frames = output_length(
            stats["frames_in"], stats["sample_rate_in"], stats["sample_rate_out"]
        )
        if stats["frames_out"] != expect_frames:
            problems.append(f"{path}: frames_out={stats['frames_out']} != {expect_frames}")
        lb, la, g = stats["lufs_before"], stats["lufs_after"], stats["gain_db"]
        if lb is not None and la is not None and abs(la - (lb + g)) > 0.5:
            problems.append(f"{path}: loudness inconsistent ({lb} + {g} != {la})")
        if target is not None and la is not None and abs(la - target) > 1.0:
            problems.append(f"{path}: lufs_after={la} deviates from target {target}")
    for prob in problems:
        print(f"problem: {prob}", file=sys.stderr)
    print(f"verify_checked={checked} problems={len(problems)}")
    return EXIT_OK if not problems else EXIT_PARTIAL


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="audiopack")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("process", help="batch convert and normalize")
    p.add_argument("--input", required=True)
    p.add_argument("--output", required=True)
    p.add_argument("--report", required=True)
    p.add_argument("--rate", type=int, required=True)
    p.add_argument("--target-lufs", type=float, required=True)
    p.add_argument("--jobs", type=int, default=1)
    p.add_argument("--peak-ceiling", type=float, default=-1.0)
    p.set_defaults(func=run_process)

    p = sub.add_parser("make-fixtures", help="generate sample input audio")
    p.add_argument("--dir", required=True)
    p.set_defaults(func=run_make_fixtures)

    p = sub.add_parser("verify", help="check a batch report")
    p.add_argument("--report", required=True)
    p.set_defaults(func=run_verify)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.command == "process" and (args.rate <= 0 or args.jobs < 1):
        print("batch error: --rate must be > 0 and --jobs >= 1", file=sys.stderr)
        return EXIT_BATCH
    return args.func(args)
