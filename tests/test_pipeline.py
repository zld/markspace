"""End-to-end pipeline tests: store, export, batch scheduling and CLI."""

from __future__ import annotations

import json
import os
import sqlite3
import subprocess
import sys
import tempfile
import unittest
import wave
from pathlib import Path

from audiocut import __main__ as cli
from audiocut.batch import run_batch
from audiocut.detector import DetectorConfig, SilenceDetector
from audiocut.exporter import ExportError, export_segments
from audiocut.fixtures import create_demo_wav, create_noise_wav, create_tone_wav
from audiocut.store import ResultStore, hash_file
from audiocut.wavio import WavFormatError, WavReader


class PipelineCase(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)

    def path(self, name: str) -> str:
        return str(self.dir / name)


class TestStore(PipelineCase):
    def test_save_and_reload_roundtrip(self) -> None:
        wav = self.path("demo.wav")
        create_demo_wav(wav)
        config = DetectorConfig()
        with WavReader(wav) as reader:
            result = SilenceDetector(config).analyze(reader)
            channels, width = reader.channels, reader.sample_width
        db = self.path("store.sqlite3")
        with ResultStore(db) as store:
            file_id = store.save_run(result, config, hash_file(wav), channels, width, wav)
            cached_id = store.find_run(hash_file(wav), config.fingerprint())
            self.assertEqual(file_id, cached_id)
            loaded = store.load_result(cached_id, wav, config)
        self.assertEqual(len(loaded.segments), len(result.segments))
        for a, b in zip(loaded.segments, result.segments):
            self.assertEqual((a.start_sample, a.end_sample), (b.start_sample, b.end_sample))
        self.assertAlmostEqual(
            loaded.silence_total_seconds, result.silence_total_seconds, places=6
        )

    def test_same_input_and_config_skips_recompute(self) -> None:
        wav = self.path("demo.wav")
        create_demo_wav(wav)
        db = self.path("store.sqlite3")
        first = run_batch([wav], DetectorConfig(), db_path=db)
        self.assertFalse(first.outcomes[0].reused_cache)
        second = run_batch([wav], DetectorConfig(), db_path=db)
        self.assertTrue(second.outcomes[0].reused_cache)

    def test_config_change_forces_recompute(self) -> None:
        wav = self.path("demo.wav")
        create_demo_wav(wav)
        db = self.path("store.sqlite3")
        run_batch([wav], DetectorConfig(frame_ms=20), db_path=db)
        run_batch([wav], DetectorConfig(frame_ms=30), db_path=db)
        with sqlite3.connect(db) as conn:
            file_rows = conn.execute("SELECT COUNT(*) FROM files").fetchone()[0]
        self.assertEqual(file_rows, 2)

    def test_interrupted_transaction_is_retryable(self) -> None:
        wav = self.path("demo.wav")
        create_demo_wav(wav)
        config = DetectorConfig()
        with WavReader(wav) as reader:
            result = SilenceDetector(config).analyze(reader)
            channels, width = reader.channels, reader.sample_width
        db = self.path("store.sqlite3")
        store = ResultStore(db)
        original = store._save_once

        def failing_once(*args, **kwargs):
            original(*args, **kwargs)  # commits, then simulate a later failure
            raise sqlite3.OperationalError("simulated interruption")

        store._save_once = failing_once  # type: ignore[method-assign]
        with self.assertRaises(RuntimeError):
            store.save_run(result, config, hash_file(wav), channels, width, wav)
        store._save_once = original  # type: ignore[method-assign]
        # Retry must succeed without a uniqueness/partial-row conflict.
        store.save_run(result, config, hash_file(wav), channels, width, wav)
        store.close()
        with sqlite3.connect(db) as conn:
            self.assertEqual(conn.execute("SELECT COUNT(*) FROM files").fetchone()[0], 1)
            self.assertEqual(conn.execute("SELECT COUNT(*) FROM runs").fetchone()[0], 1)
            self.assertEqual(
                conn.execute("SELECT COUNT(*) FROM segments").fetchone()[0],
                len(result.segments),
            )


class TestExport(PipelineCase):
    def test_export_creates_slices_and_manifest(self) -> None:
        wav = self.path("demo.wav")
        create_demo_wav(wav)
        outdir = self.path("slices")
        report = run_batch([wav], DetectorConfig(), outdir=outdir)
        result = report.outcomes[0].result
        exported = sorted(Path(outdir).glob("*.wav"))
        self.assertEqual(len(exported), len(result.segments))
        manifest = json.load(open(Path(outdir) / "demo_manifest.json"))
        self.assertEqual(len(manifest["segments"]), len(result.segments))
        for exported_wav, segment in zip(exported, result.segments):
            with wave.open(str(exported_wav)) as wf:
                self.assertEqual(wf.getnframes(), segment.length)
        # No temporary files left behind.
        self.assertEqual(list(Path(outdir).glob("*.tmp")), [])
        self.assertEqual(list(Path(outdir).glob(".audiocut-*")), [])

    def test_refuse_source_inside_outdir(self) -> None:
        outdir = self.dir
        wav = str(outdir / "inside.wav")
        create_demo_wav(wav)
        with WavReader(wav) as reader:
            result = SilenceDetector().analyze(reader)
        with self.assertRaises(ExportError):
            export_segments(wav, result, str(outdir))

    def test_path_escape_rejected(self) -> None:
        wav = self.path("demo.wav")
        create_demo_wav(wav)
        with WavReader(wav) as reader:
            result = SilenceDetector().analyze(reader)
        # The manifest path itself is confined; craft an escaping target directly.
        from audiocut.exporter import _resolve_inside

        with self.assertRaises(ExportError):
            _resolve_inside(self.dir, self.dir / ".." / "escape.wav")


class TestBatchIsolation(PipelineCase):
    def test_one_bad_file_does_not_block_others(self) -> None:
        good = self.path("good.wav")
        create_tone_wav(good)
        bad = self.path("bad.wav")
        with open(bad, "wb") as handle:
            handle.write(b"RIFFxxxxWAVEgarbage")
        report = run_batch([bad, good], DetectorConfig())
        self.assertTrue(report.has_errors)
        statuses = {o.path: o.status for o in report.outcomes}
        self.assertEqual(statuses[bad], "error")
        self.assertEqual(statuses[good], "ok")
        self.assertIn("bad.wav", report.outcomes[0].error)

    def test_directory_scan_finds_wavs(self) -> None:
        create_tone_wav(self.path("a.wav"))
        create_noise_wav(self.path("b.wav"))
        nested = self.dir / "nested"
        nested.mkdir()
        create_tone_wav(str(nested / "c.wav"))
        report = run_batch([str(self.dir)], DetectorConfig())
        names = sorted(Path(o.path).name for o in report.outcomes)
        self.assertEqual(names, ["a.wav", "b.wav", "c.wav"])

    def test_analyze_and_export_share_one_detector_result(self) -> None:
        wav = self.path("demo.wav")
        create_demo_wav(wav)
        config = DetectorConfig()
        analyze_report = run_batch([wav], config)
        export_report = run_batch([wav], config, outdir=self.path("out"))
        a = analyze_report.outcomes[0].result
        b = export_report.outcomes[0].result
        self.assertEqual(
            [(s.start_sample, s.end_sample) for s in a.segments],
            [(s.start_sample, s.end_sample) for s in b.segments],
        )
        self.assertEqual(a.config_fingerprint, b.config_fingerprint)


class TestCli(PipelineCase):
    def test_text_and_json_output(self) -> None:
        wav = self.path("demo.wav")
        create_demo_wav(wav)
        code = cli.main(["analyze", wav])
        self.assertEqual(code, 0)
        import contextlib
        import io

        buffer = io.StringIO()
        with contextlib.redirect_stdout(buffer):
            code = cli.main(["analyze", "--json", wav])
        self.assertEqual(code, 0)
        payload = json.loads(buffer.getvalue())
        self.assertEqual(len(payload["results"]), 1)
        self.assertEqual(len(payload["results"][0]["segments"]), 3)

    def test_parameter_error_returns_1(self) -> None:
        wav = self.path("demo.wav")
        create_demo_wav(wav)
        code = cli.main(["analyze", "--frame-ms", "-5", wav])
        self.assertEqual(code, 1)
        code = cli.main(["analyze", "--merge-gap-s", "0.01", "--min-silence-s", "0.5", wav])
        self.assertEqual(code, 1)

    def test_corrupt_input_returns_2(self) -> None:
        bad = self.path("bad.wav")
        with open(bad, "wb") as handle:
            handle.write(b"not a wav")
        self.assertEqual(cli.main(["analyze", bad]), 2)

    def test_demo_command_end_to_end(self) -> None:
        workdir = self.path("demo-work")
        code = cli.main(["demo", "--workdir", workdir])
        self.assertEqual(code, 0)
        source = Path(workdir) / "demo_voice.wav"
        self.assertTrue(source.exists())
        with wave.open(str(source)) as wf:
            self.assertEqual(wf.getframerate(), 16000)
            self.assertEqual(wf.getnframes(), 72000)
        slices = list((Path(workdir) / "slices").glob("*.wav"))
        self.assertEqual(len(slices), 3)
        self.assertTrue((Path(workdir) / "audiocut.sqlite3").exists())


class TestProcessInvocation(unittest.TestCase):
    """Sanity-check the real console entry point in a subprocess."""

    def test_module_invocation(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            proc = subprocess.run(
                [sys.executable, "-m", "audiocut", "demo", "--workdir", tmp],
                capture_output=True, text=True, check=False,
            )
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertIn("segments=3", proc.stdout)
            self.assertIn("silence_total_seconds=1.5", proc.stdout)


if __name__ == "__main__":
    unittest.main()
