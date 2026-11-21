"""Discovery, conflict detection and resume tests."""

from __future__ import annotations

import json
import os
import unittest

import numpy as np
import soundfile as sf

from audiorepro.discover import discover, sha256_file
from audiorepro.fixtures import generate
from audiorepro.params import PipelineParams
from audiorepro.pipeline import run_pipeline
from audiorepro.processing import process_file
from audiorepro.resample import Resampler, ResamplerConfig

from support import TempDirTest, write_audio


class DiscoverTests(TempDirTest):
    def setUp(self) -> None:
        super().setUp()
        self.input = self.tmp / "input"

    def test_scan_is_sorted_recursive_and_format_aware(self) -> None:
        write_audio(self.input / "b.wav", np.zeros((100, 1)), 48000)
        write_audio(self.input / "a" / "c.flac", np.zeros((100, 2)), 48000)
        write_audio(self.input / "a" / "d.aiff", np.zeros((100, 2)), 48000)
        (self.input / "notes.txt").write_text("ignored")
        result = discover(self.input)
        self.assertEqual([f.relative_path for f in result.files], ["a/c.flac", "a/d.aiff", "b.wav"])
        self.assertEqual([f.ext_format for f in result.files], ["FLAC", "AIFF", "WAV"])
        self.assertEqual(result.failed_count, 0)

    def test_symlink_is_rejected(self) -> None:
        target = write_audio(self.tmp / "outside.wav", np.zeros((100, 1)), 48000)
        self.input.mkdir()
        os.symlink(target, self.input / "link.wav")
        result = discover(self.input)
        self.assertEqual(result.files, [])
        self.assertEqual(result.errors[0].stage, "discover")
        self.assertIn("symbolic link", result.errors[0].reason)

    def test_corrupt_file_does_not_stop_scan(self) -> None:
        write_audio(self.input / "good.wav", np.zeros((100, 1)), 48000)
        (self.input / "bad.wav").write_bytes(b"RIFF....WAVEjunk")
        result = discover(self.input)
        self.assertEqual([f.relative_path for f in result.files], ["good.wav"])
        self.assertEqual([e.path for e in result.errors], ["bad.wav"])

    def test_output_name_collision_is_reported(self) -> None:
        write_audio(self.input / "take.wav", np.zeros((100, 1)), 48000)
        write_audio(self.input / "take.aiff", np.zeros((100, 1)), 48000)
        result = discover(self.input)
        self.assertEqual(result.files, [])
        self.assertEqual({e.path for e in result.errors}, {"take.aiff", "take.wav"})
        self.assertTrue(all("collides" in e.reason for e in result.errors))

    def test_same_stem_in_different_directories_is_allowed(self) -> None:
        write_audio(self.input / "one" / "take.wav", np.zeros((100, 1)), 48000)
        write_audio(self.input / "two" / "take.wav", np.zeros((100, 1)), 48000)
        result = discover(self.input)
        self.assertEqual(len(result.files), 2)
        self.assertEqual(result.errors, [])

    def test_unsupported_rate_and_layout_are_rejected(self) -> None:
        write_audio(self.input / "low.wav", np.zeros((100, 1)), 4000)
        write_audio(self.input / "quad.wav", np.zeros((100, 4)), 48000)
        write_audio(self.input / "empty.wav", np.zeros((0, 1)), 48000)
        result = discover(self.input)
        self.assertEqual(result.files, [])
        self.assertEqual(len(result.errors), 3)
        reasons = " ".join(e.reason for e in result.errors)
        self.assertIn("sample rate", reasons)
        self.assertIn("channel layout", reasons)
        self.assertIn("no audio frames", reasons)

    def test_symlinked_directory_is_rejected(self) -> None:
        real = self.tmp / "real"
        write_audio(real / "a.wav", np.zeros((100, 1)), 48000)
        self.input.mkdir()
        os.symlink(real, self.input / "linked")
        result = discover(self.input)
        self.assertEqual(result.files, [])
        self.assertEqual([e.path for e in result.errors], ["linked"])

    def test_missing_input_directory_is_a_usage_error(self) -> None:
        from audiorepro.errors import UsageError

        with self.assertRaises(UsageError):
            discover(self.tmp / "does-not-exist")

    def test_hash_is_content_stable(self) -> None:
        path = write_audio(self.input / "x.wav", np.zeros((100, 1)), 48000)
        self.assertEqual(sha256_file(path), sha256_file(path))
        self.assertEqual(len(sha256_file(path)), 64)


class PipelineResumeTests(TempDirTest):
    def _params(self, **overrides) -> PipelineParams:
        base = dict(
            input_dir=self.tmp / "input",
            output_dir=self.tmp / "output",
            sample_rate=48000,
            target_lufs=-23.0,
            peak_dbfs=-1.0,
            jobs=1,
            resume=False,
            chunk_frames=4096,
            resampler=ResamplerConfig(half_width=64, phases=256),
        )
        base.update(overrides)
        return PipelineParams(**base)

    def test_resume_skips_unchanged_files(self) -> None:
        generate(self.tmp / "input", seed=17)
        run_pipeline(self._params(), self.tmp / "report.json")
        resumed = run_pipeline(self._params(resume=True), self.tmp / "report2.json")
        self.assertEqual(resumed.report["summary"]["resumed"], 6)
        self.assertEqual(resumed.report["summary"]["failed"], 0)

    def test_resume_detects_changed_source(self) -> None:
        generate(self.tmp / "input", seed=17)
        run_pipeline(self._params(), self.tmp / "report.json")
        source = self.tmp / "input" / "mono" / "mono_tone.wav"
        data, rate = sf.read(str(source), always_2d=True)
        sf.write(str(source), data * 0.5, rate, subtype="PCM_24")
        result = run_pipeline(self._params(resume=True), self.tmp / "report2.json")
        self.assertEqual(result.report["summary"]["resumed"], 5)

    def test_resume_does_not_trust_missing_output(self) -> None:
        generate(self.tmp / "input", seed=17)
        run_pipeline(self._params(), self.tmp / "report.json")
        (self.tmp / "output" / "mono" / "mono_tone.wav").unlink()
        result = run_pipeline(self._params(resume=True), self.tmp / "report2.json")
        self.assertEqual(result.report["summary"]["resumed"], 5)
        self.assertTrue((self.tmp / "output" / "mono" / "mono_tone.wav").is_file())

    def test_changed_parameters_invalidate_resume(self) -> None:
        generate(self.tmp / "input", seed=17)
        run_pipeline(self._params(), self.tmp / "report.json")
        result = run_pipeline(
            self._params(resume=True, target_lufs=-20.0), self.tmp / "report2.json"
        )
        self.assertEqual(result.report["summary"]["resumed"], 0)

    def test_state_is_written_next_to_outputs(self) -> None:
        generate(self.tmp / "input", seed=17)
        run_pipeline(self._params(), self.tmp / "report.json")
        state = json.loads((self.tmp / "output" / ".audiorepro-state.json").read_text())
        self.assertEqual(state["schema"], "audiorepro.state/1")
        self.assertEqual(len(state["entries"]), 6)


class SafetyTests(TempDirTest):
    def test_refuses_to_overwrite_source(self) -> None:
        source = write_audio(self.tmp / "same" / "a.wav", np.zeros((4800, 1)), 48000)
        params = PipelineParams(
            input_dir=self.tmp / "same",
            output_dir=self.tmp / "same",
            sample_rate=48000,
            target_lufs=-23.0,
            peak_dbfs=-1.0,
            chunk_frames=2048,
        )
        found = discover(params.input_dir).files
        self.assertEqual(len(found), 1)
        with self.assertRaises(Exception):
            process_file(found[0], params, Resampler())
        self.assertTrue(source.is_file())

    def test_failed_same_path_run_preserves_source(self) -> None:
        from audiorepro.pipeline import ResumeState, _discard_stale

        source = write_audio(self.tmp / "same" / "a.wav", np.zeros((4800, 1)), 48000)
        params = PipelineParams(
            input_dir=self.tmp / "same",
            output_dir=self.tmp / "same",
            sample_rate=48000,
            target_lufs=-23.0,
            peak_dbfs=-1.0,
            chunk_frames=2048,
        )
        item = discover(params.input_dir).files[0]
        state = ResumeState(self.tmp / "state.json")
        state.entries[item.relative_path] = {"recorded": {}}
        _discard_stale(item, params, state)
        self.assertNotIn(item.relative_path, state.entries)
        self.assertTrue(source.is_file())
        self.assertGreater(source.stat().st_size, 0)

    def test_source_files_are_never_modified(self) -> None:
        generate(self.tmp / "input", seed=17)
        before = {
            path.relative_to(self.tmp).as_posix(): sha256_file(path)
            for path in (self.tmp / "input").rglob("*")
            if path.is_file()
        }
        params = PipelineParams(
            input_dir=self.tmp / "input",
            output_dir=self.tmp / "output",
            sample_rate=48000,
            target_lufs=-23.0,
            peak_dbfs=-1.0,
            chunk_frames=4096,
            resampler=ResamplerConfig(half_width=64, phases=256),
        )
        run_pipeline(params, self.tmp / "report.json")
        after = {
            path.relative_to(self.tmp).as_posix(): sha256_file(path)
            for path in (self.tmp / "input").rglob("*")
            if path.is_file()
        }
        self.assertEqual(before, after)

    def test_no_staging_files_remain(self) -> None:
        generate(self.tmp / "input", seed=17)
        params = PipelineParams(
            input_dir=self.tmp / "input",
            output_dir=self.tmp / "output",
            sample_rate=48000,
            target_lufs=-23.0,
            peak_dbfs=-1.0,
            jobs=2,
            chunk_frames=4096,
            resampler=ResamplerConfig(half_width=64, phases=256),
        )
        run_pipeline(params, self.tmp / "report.json")
        leftovers = [p.name for p in (self.tmp / "output").rglob(".*") if p.name.startswith(".")]
        self.assertEqual(leftovers, [".audiorepro-state.json"])


if __name__ == "__main__":
    unittest.main()
