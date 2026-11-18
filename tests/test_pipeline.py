"""End-to-end batch pipeline tests: exit codes, error isolation, normalization."""

import json

import numpy as np
import pytest
import soundfile as sf

from audiopack.cli import EXIT_BATCH, EXIT_OK, EXIT_PARTIAL, main

RATE = 44100
OUT_RATE = 48000


def write_wav(path, data, rate=RATE, subtype="PCM_16"):
    path.parent.mkdir(parents=True, exist_ok=True)
    sf.write(path, data, rate, subtype=subtype)
    return path


def sine(freq, rate, frames, amp=0.3):
    t = np.arange(frames) / rate
    return amp * np.sin(2 * np.pi * freq * t)


def stereo(freq_l=220, freq_r=330, frames=RATE // 2, amp=0.3):
    return np.stack(
        [sine(freq_l, RATE, frames, amp), sine(freq_r, RATE, frames, amp)], axis=1
    )


def run_process(input_dir, output_dir, report, **overrides):
    argv = [
        "process",
        "--input", str(input_dir),
        "--output", str(output_dir),
        "--report", str(report),
        "--rate", str(overrides.pop("rate", OUT_RATE)),
        "--target-lufs", str(overrides.pop("target_lufs", -18.0)),
        "--jobs", str(overrides.pop("jobs", 2)),
    ]
    for key, value in overrides.items():
        argv += [f"--{key.replace('_', '-')}", str(value)]
    return main(argv)


class TestBatchHappyPath:
    def test_all_files_ok(self, tmp_path):
        src = tmp_path / "in"
        write_wav(src / "a.wav", stereo())
        write_wav(src / "sub" / "b.flac", stereo(frames=RATE // 4), subtype="PCM_16")
        report = tmp_path / "report.json"
        code = run_process(src, tmp_path / "out", report)
        assert code == EXIT_OK

        doc = json.loads(report.read_text())
        assert doc["summary"] == {"files_ok": 2, "files_failed": 0}
        paths = [f["path"] for f in doc["files"]]
        assert paths == sorted(paths) == ["a.wav", "sub/b.wav"]
        for entry in doc["files"]:
            stats = entry["stats"]
            assert stats["sample_rate_in"] == RATE
            assert stats["sample_rate_out"] == OUT_RATE
            assert stats["channels"] == 2
            assert stats["clipped_samples_after"] == 0
            assert stats["lufs_after"] == pytest.approx(-18.0, abs=0.5)
            assert stats["rms_after_dbfs"] > stats["rms_before_dbfs"] - 30
            out = tmp_path / "out" / entry["path"]
            info = sf.info(out)
            assert info.samplerate == OUT_RATE
            assert info.subtype == "PCM_24"

    def test_silence_is_not_amplified(self, tmp_path):
        src = tmp_path / "in"
        write_wav(src / "silence.wav", np.zeros((RATE // 2, 2), dtype=np.float32))
        report = tmp_path / "report.json"
        assert run_process(src, tmp_path / "out", report) == EXIT_OK
        stats = json.loads(report.read_text())["files"][0]["stats"]
        assert stats["gain_db"] == 0.0
        assert stats["lufs_after"] is None  # -inf sanitized to null
        data = sf.read(tmp_path / "out" / "silence.wav", always_2d=True)[0]
        assert np.max(np.abs(data)) == 0.0

    def test_peak_ceiling_limits_gain(self, tmp_path):
        src = tmp_path / "in"
        loud = stereo(frames=RATE, amp=0.9)
        write_wav(src / "hot.wav", loud)
        report = tmp_path / "report.json"
        assert run_process(src, tmp_path / "out", report, target_lufs=-6.0) == EXIT_OK
        stats = json.loads(report.read_text())["files"][0]["stats"]
        assert stats["peak_after_dbfs"] <= stats["peak_before_dbfs"] + stats["gain_db"] + 0.1
        assert stats["gain_db"] <= -1.0 - stats["peak_before_dbfs"] + 0.1
        assert stats["clipped_samples_after"] == 0


class TestPerFileFailures:
    def test_corrupt_file_fails_others_continue(self, tmp_path):
        src = tmp_path / "in"
        write_wav(src / "good.wav", stereo())
        (src / "broken.wav").write_bytes(b"not a real wav file")
        report = tmp_path / "report.json"
        code = run_process(src, tmp_path / "out", report)
        assert code == EXIT_PARTIAL
        doc = json.loads(report.read_text())
        assert doc["summary"] == {"files_ok": 1, "files_failed": 1}
        failed = [f for f in doc["files"] if f["status"] == "failed"]
        assert failed[0]["path"] == "broken.wav"
        assert failed[0]["error"]
        assert not (tmp_path / "out" / "broken.wav").exists()

    def test_zero_frame_file_rejected(self, tmp_path):
        src = tmp_path / "in"
        write_wav(src / "ok.wav", stereo())
        with sf.SoundFile(src / "empty.wav", "w", samplerate=RATE, channels=2):
            pass
        report = tmp_path / "report.json"
        assert run_process(src, tmp_path / "out", report) == EXIT_PARTIAL
        doc = json.loads(report.read_text())
        assert doc["summary"]["files_failed"] == 1

    def test_extension_format_mismatch_rejected(self, tmp_path):
        src = tmp_path / "in"
        write_wav(src / "ok.wav", stereo())
        # FLAC content masquerading as .wav.
        data = stereo(frames=1024)
        sf.write(src / "fake.wav", data, RATE, format="FLAC")
        report = tmp_path / "report.json"
        assert run_process(src, tmp_path / "out", report) == EXIT_PARTIAL
        doc = json.loads(report.read_text())
        errors = [f["error"] for f in doc["files"] if f["status"] == "failed"]
        assert any("mismatch" in e for e in errors)

    def test_too_many_channels_rejected(self, tmp_path):
        src = tmp_path / "in"
        write_wav(src / "ok.wav", stereo())
        write_wav(src / "wide.wav", np.zeros((1024, 16), dtype=np.float32))
        report = tmp_path / "report.json"
        assert run_process(src, tmp_path / "out", report) == EXIT_PARTIAL
        doc = json.loads(report.read_text())
        errors = [f["error"] for f in doc["files"] if f["status"] == "failed"]
        assert any("channel" in e for e in errors)


class TestBatchLevelErrors:
    def test_missing_input_dir(self, tmp_path):
        code = run_process(tmp_path / "nope", tmp_path / "out", tmp_path / "r.json")
        assert code == EXIT_BATCH

    def test_overlapping_directories(self, tmp_path):
        src = tmp_path / "in"
        write_wav(src / "a.wav", stereo())
        code = run_process(src, src / "nested", tmp_path / "r.json")
        assert code == EXIT_BATCH

    def test_existing_output_not_overwritten(self, tmp_path):
        src = tmp_path / "in"
        write_wav(src / "a.wav", stereo())
        out = tmp_path / "out"
        out.mkdir()
        sentinel = out / "a.wav"
        sentinel.write_bytes(b"keep me")
        code = run_process(src, out, tmp_path / "r.json")
        assert code == EXIT_BATCH
        assert sentinel.read_bytes() == b"keep me"

    def test_duplicate_output_paths(self, tmp_path):
        src = tmp_path / "in"
        write_wav(src / "dup.wav", stereo())
        write_wav(src / "dup.flac", stereo(frames=1024))
        code = run_process(src, tmp_path / "out", tmp_path / "r.json")
        assert code == EXIT_BATCH

    def test_invalid_rate_argument(self, tmp_path):
        src = tmp_path / "in"
        write_wav(src / "a.wav", stereo())
        code = run_process(src, tmp_path / "out", tmp_path / "r.json", rate=0)
        assert code == EXIT_BATCH


class TestReportIntegrity:
    def test_report_written_atomically_on_partial_failure(self, tmp_path):
        src = tmp_path / "in"
        write_wav(src / "good.wav", stereo())
        (src / "bad.wav").write_bytes(b"junk")
        report = tmp_path / "report.json"
        assert run_process(src, tmp_path / "out", report) == EXIT_PARTIAL
        doc = json.loads(report.read_text())
        assert doc["version"] == 1
        assert doc["config"]["sample_rate"] == OUT_RATE
        # No temporary files left behind.
        leftovers = [p for p in tmp_path.rglob("*") if p.suffix == ".tmp"]
        assert leftovers == []

    def test_verify_subcommand(self, tmp_path, capsys):
        src = tmp_path / "in"
        write_wav(src / "a.wav", stereo())
        report = tmp_path / "report.json"
        assert run_process(src, tmp_path / "out", report) == EXIT_OK
        assert main(["verify", "--report", str(report)]) == EXIT_OK
        assert "problems=0" in capsys.readouterr().out
        # Tampered report is flagged.
        doc = json.loads(report.read_text())
        doc["files"][0]["stats"]["clipped_samples_after"] = 5
        report.write_text(json.dumps(doc))
        assert main(["verify", "--report", str(report)]) == EXIT_PARTIAL
