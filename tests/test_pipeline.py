"""End-to-end tests: detection, SQLite storage, re-read and JSONL export."""

import json
import sqlite3
import sys
import tempfile
import unittest
import wave
from pathlib import Path

import numpy as np

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "src"))

from audioslice import cli  # noqa: E402
from audioslice.detector import detect  # noqa: E402
from audioslice.export import export_tasks  # noqa: E402
from audioslice.model import AnalysisParams  # noqa: E402
from audioslice.store import connect, latest_task_id, list_task_ids, load_task, save_task  # noqa: E402
from audioslice.wav import WavFormatError, load_wav  # noqa: E402
from scripts.make_fixture import SAMPLE_RATE, build_dialogue, write_pcm16  # noqa: E402


class PipelineTestBase(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.wav_path = self.root / "dialogue.wav"
        self.db_path = self.root / "audio.sqlite"
        write_pcm16(self.wav_path, build_dialogue())

    def analyze_params(self) -> AnalysisParams:
        # Mirror the acceptance invocation: 10 ms frames, 180 ms silence, 90 ms segment.
        return AnalysisParams.from_milliseconds(
            sample_rate=SAMPLE_RATE, frame_ms=10.0, min_segment_ms=90.0, min_silence_ms=180.0
        )


class FullPipelineTests(PipelineTestBase):
    def test_detect_store_reread_export_roundtrip(self) -> None:
        audio = load_wav(str(self.wav_path))
        params = self.analyze_params()
        result = detect(audio, params)

        self.assertEqual(len(result.segments), 3)
        for region in result.segments:
            # Each line starts well inside the file and spans a non-trivial
            # duration that meets the configured minimum segment length.
            self.assertGreaterEqual(region.start_sample, 8000)
            self.assertLess(region.end_sample, audio.samples.shape[0])
            self.assertGreater(region.end_sample, region.start_sample)
            self.assertGreaterEqual(region.length_samples, params.min_segment_samples)

        conn = connect(str(self.db_path))
        task_id = save_task(conn, str(self.wav_path), params, result, audio.channels, audio.sample_width_bytes)
        conn.close()

        conn = connect(str(self.db_path))
        reloaded = load_task(conn, task_id)
        self.assertIsNotNone(reloaded)
        self.assertEqual(reloaded.segment_count, 3)
        self.assertEqual(
            [(r.start_sample, r.end_sample) for r in reloaded.segments],
            [(r.start_sample, r.end_sample) for r in result.segments],
        )
        self.assertEqual(
            [r.length_samples for r in reloaded.silences],
            [r.length_samples for r in result.silences],
        )

        out_path = self.root / "out.jsonl"
        line_count = export_tasks(conn, [task_id], str(out_path))
        conn.close()

        lines = out_path.read_text(encoding="utf-8").strip().split("\n")
        self.assertEqual(line_count, len(lines))
        records = [json.loads(line) for line in lines]
        self.assertEqual(sum(1 for r in records if r["type"] == "segment"), 3)
        self.assertTrue(all("start_sample" in r and "end_sample" in r for r in records))
        silence_seconds = sum(
            r["length_samples"] for r in records if r["type"] == "silence"
        ) / SAMPLE_RATE
        self.assertTrue(0.8 <= silence_seconds <= 1.5, f"unexpected silence total {silence_seconds}")

    def test_repeated_input_creates_new_task_without_overwrite(self) -> None:
        audio = load_wav(str(self.wav_path))
        params = self.analyze_params()
        result = detect(audio, params)
        conn = connect(str(self.db_path))
        first = save_task(conn, str(self.wav_path), params, result, 1, 2)
        second = save_task(conn, str(self.wav_path), params, result, 1, 2)
        self.assertNotEqual(first, second)
        self.assertEqual(list_task_ids(conn), [first, second])
        self.assertEqual(latest_task_id(conn), second)
        # First task's rows remain intact.
        self.assertEqual(load_task(conn, first).segment_count, 3)
        conn.close()

    def test_write_failure_rolls_back_entire_task(self) -> None:
        audio = load_wav(str(self.wav_path))
        result = detect(audio, self.analyze_params())
        conn = connect(str(self.db_path))
        sabotage = (
            "CREATE TRIGGER sabotage BEFORE INSERT ON segments "
            "BEGIN SELECT RAISE(FAIL, 'injected failure'); END"
        )
        conn.execute(sabotage)
        with self.assertRaises(sqlite3.Error):
            save_task(conn, str(self.wav_path), self.analyze_params(), result, 1, 2)
        self.assertEqual(list_task_ids(conn), [])
        self.assertEqual(conn.execute("SELECT COUNT(*) FROM segments").fetchone()[0], 0)
        self.assertEqual(conn.execute("SELECT COUNT(*) FROM silences").fetchone()[0], 0)
        conn.close()

    def test_export_unknown_task_id_raises(self) -> None:
        conn = connect(str(self.db_path))
        with self.assertRaises(KeyError):
            export_tasks(conn, [999], str(self.root / "x.jsonl"))
        conn.close()


class WavValidationTests(PipelineTestBase):
    def _write_wav(self, path: Path, frames: bytes, channels: int = 1, width: int = 2, rate: int = 24000) -> None:
        with wave.open(str(path), "wb") as handle:
            handle.setnchannels(channels)
            handle.setsampwidth(width)
            handle.setframerate(rate)
            handle.writeframes(frames)

    def test_stereo_downmix_uses_same_rules(self) -> None:
        mono = build_dialogue()
        stereo = np.repeat(mono[:, None], 2, axis=1)
        stereo_path = self.root / "stereo.wav"
        pcm = np.round(np.clip(stereo, -1, 1) * 32767).astype("<i2")
        self._write_wav(stereo_path, pcm.tobytes(), channels=2)
        audio = load_wav(str(stereo_path))
        self.assertEqual(audio.channels, 2)
        self.assertEqual(audio.samples.ndim, 1)
        result = detect(audio, self.analyze_params())
        mono_audio = load_wav(str(self.wav_path))
        mono_result = detect(mono_audio, self.analyze_params())
        self.assertEqual(
            [(r.start_sample, r.end_sample) for r in result.segments],
            [(r.start_sample, r.end_sample) for r in mono_result.segments],
        )

    def test_corrupt_file_rejected(self) -> None:
        bad = self.root / "bad.wav"
        bad.write_bytes(b"this is definitely not a wav file" * 10)
        with self.assertRaises(WavFormatError):
            load_wav(str(bad))

    def test_unsupported_compression_rejected(self) -> None:
        path = self.root / "float.wav"
        # WAVE_FORMAT_IEEE_FLOAT tag (3), otherwise well-formed headers.
        header = b"RIFF" + (36).to_bytes(4, "little") + b"WAVEfmt " + (16).to_bytes(4, "little")
        header += (3).to_bytes(2, "little") + (1).to_bytes(2, "little")
        header += (8000).to_bytes(4, "little") + (16000).to_bytes(4, "little")
        header += (2).to_bytes(2, "little") + (16).to_bytes(2, "little") + b"data" + (0).to_bytes(4, "little")
        path.write_bytes(header)
        with self.assertRaises(WavFormatError):
            load_wav(str(path))

    def test_truncated_data_rejected(self) -> None:
        path = self.root / "trunc.wav"
        self._write_wav(path, (np.zeros(100, dtype="<i2")).tobytes())
        raw = path.read_bytes()
        path.write_bytes(raw[:-20])  # chop declared frames off the tail
        with self.assertRaises(WavFormatError):
            load_wav(str(path))

    def test_missing_file_raises_filenotfound(self) -> None:
        with self.assertRaises(FileNotFoundError):
            load_wav(str(self.root / "missing.wav"))


class CliTests(PipelineTestBase):
    def test_analyze_summary_export_exit_codes_and_output(self) -> None:
        code = cli.main([
            "analyze", "--input", str(self.wav_path), "--db", str(self.db_path),
            "--frame-ms", "10", "--min-silence-ms", "180", "--min-segment-ms", "90",
        ])
        self.assertEqual(code, 0)
        code = cli.main(["summary", "--db", str(self.db_path), "--latest"])
        self.assertEqual(code, 0)
        out = self.root / "export.jsonl"
        code = cli.main(["export", "--db", str(self.db_path), "--out", str(out), "--all"])
        self.assertEqual(code, 0)
        self.assertTrue(out.exists())

    def test_bad_input_returns_exit_code_2(self) -> None:
        bad = self.root / "bad.wav"
        bad.write_bytes(b"not a wav" * 8)
        code = cli.main(["analyze", "--input", str(bad), "--db", str(self.db_path)])
        self.assertEqual(code, 2)

    def test_missing_argument_returns_exit_code_2(self) -> None:
        with self.assertRaises(SystemExit) as ctx:
            cli.main(["analyze", "--input", str(self.wav_path)])  # no --db
        self.assertEqual(ctx.exception.code, 2)

    def test_summary_unknown_task_returns_2(self) -> None:
        connect(str(self.db_path)).close()
        code = cli.main(["summary", "--db", str(self.db_path), "--task-id", "42"])
        self.assertEqual(code, 2)


if __name__ == "__main__":
    unittest.main()
