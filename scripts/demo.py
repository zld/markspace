"""Generate demo material and run the acceptance flow.

Usage: .venv/bin/python scripts/demo.py --work-dir .demo

Creates three PCM files under <work-dir>/audio (mixed sample rates, mono and
stereo, fades, a short burst that must be dropped, low noise floor), runs the
batch pipeline into <work-dir>/results.sqlite, prints the summary, and then
verifies that the single-file path yields identical segment boundaries.
"""
from __future__ import annotations

import argparse
import sys
from pathlib import Path

from audioslice import audio
from audioslice.batch import BatchConfig, process_file, run_batch
from audioslice.cli import _print_batch_human
from audioslice.detector import DetectorParams
from audioslice.segments import SegmentParams


def generate(audio_dir: Path) -> None:
    audio_dir.mkdir(parents=True, exist_ok=True)
    # 16 kHz mono rehearsal take: two phrases with fades, one 30 ms cough
    # (burst) that must not become a segment, low constant noise floor.
    audio.synth_file(audio_dir / "01_rehearsal.wav", 16_000, 6.0, [
        audio.SynthEvent(0.6, 1.2, freq=220.0, amp=0.35,
                         fade_in=0.4, fade_out=0.3),
        audio.SynthEvent(2.6, 0.03, freq=900.0, amp=0.5),          # burst
        audio.SynthEvent(3.8, 1.0, freq=260.0, amp=0.30,
                         fade_in=0.2, fade_out=0.4),
    ], noise_rms=0.003, seed=101)
    # 44.1 kHz stereo ambience take: two events, one swelling slowly.
    audio.synth_file(audio_dir / "02_ambience.wav", 44_100, 5.0, [
        audio.SynthEvent(0.8, 1.5, freq=180.0, amp=0.25,
                         fade_in=0.6, fade_out=0.5),
        audio.SynthEvent(3.2, 0.9, freq=520.0, amp=0.40),
    ], channels=2, noise_rms=0.004, seed=202)
    # 8 kHz mono voice memo: two short utterances at the lowest supported rate.
    audio.synth_file(audio_dir / "03_memo.wav", 8_000, 4.0, [
        audio.SynthEvent(0.5, 0.8, freq=300.0, amp=0.35, fade_in=0.15),
        audio.SynthEvent(2.4, 0.7, freq=350.0, amp=0.30, fade_out=0.25),
    ], noise_rms=0.003, seed=303)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work-dir", default=".demo")
    args = parser.parse_args()
    work = Path(args.work_dir)
    audio_dir = work / "audio"
    db_path = work / "results.sqlite"

    print(f"generating demo material under {audio_dir}")
    generate(audio_dir)

    config = BatchConfig(detector=DetectorParams(), segments=SegmentParams())
    summary = run_batch(audio_dir, db_path, config)
    _print_batch_human(summary)
    if summary["failed"]:
        print("demo FAILED: some files did not process", file=sys.stderr)
        return 4

    # Acceptance cross-check: single-file path must reproduce batch bounds.
    for res in summary["results"]:
        single = process_file(res["path"], config)
        got = [(s["start_sample"], s["end_sample"]) for s in single["segments"]]
        want = [(s["start_sample"], s["end_sample"]) for s in res["segments"]]
        if got != want:
            print(f"demo FAILED: single/batch mismatch for {res['path']}",
                  file=sys.stderr)
            return 1
    print(f"consistency check OK: single-file boundaries match batch for "
          f"{summary['files']} files, {summary['segments']} segments")
    print(f"next: .venv/bin/python -m audioslice batch --input {audio_dir} "
          f"--db {db_path} --json")
    return 0


if __name__ == "__main__":
    sys.exit(main())
