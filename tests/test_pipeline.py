"""End-to-end tests: WAV validation, batch pipeline, store and export."""
from __future__ import annotations

import json
import wave
from pathlib import Path

import numpy as np
import pytest

from audiosplit.batch import find_wav_files, run_batch
from audiosplit.export import export
from audiosplit.models import DetectParams
from audiosplit.store import Store
from audiosplit.wav import WavError, read_wav

SR = 16000


def _write_wav(path: Path, samples: np.ndarray, sr: int = SR,
               channels: int = 1, width: int = 2, *, truncate_bytes: int = 0,
               non_pcm: bool = False) -> None:
    if samples.ndim == 1:
        samples = samples.reshape(-1, 1)
    pcm = np.clip(samples.reshape(-1), -1, 1 - 1 / 32768)
    full_raw = (pcm * 32768).astype("<i2").tobytes()
    raw = full_raw[:-truncate_bytes] if truncate_bytes else full_raw
    # Hand-built RIFF so the data chunk size can lie about truncation and the
    # format tag can advertise a non-PCM codec the stdlib writer refuses.
    fmt_tag = 7 if non_pcm else 1  # 7 = mu-law, anything but PCM (1)
    fmt_chunk = (
        fmt_tag.to_bytes(2, "little")
        + channels.to_bytes(2, "little")
        + sr.to_bytes(4, "little")
        + (sr * channels * width).to_bytes(4, "little")
        + (channels * width).to_bytes(2, "little")
        + (width * 8).to_bytes(2, "little")
    )
    # The chunk header keeps the ORIGINAL size; truncated payload then
    # disagrees with it, mimicking a cut-off recording.
    riff_size = 4 + (8 + len(fmt_chunk)) + (8 + len(full_raw))
    header = b"RIFF" + riff_size.to_bytes(4, "little") + b"WAVE"
    header += b"fmt " + len(fmt_chunk).to_bytes(4, "little") + fmt_chunk
    header += b"data" + len(full_raw).to_bytes(4, "little")
    path.write_bytes(header + raw)


@pytest.fixture
def rng() -> np.random.Generator:
    return np.random.default_rng(11)


@pytest.fixture
def wav_dir(tmp_path: Path, rng) -> Path:
    root = tmp_path / "audio"
    sub = root / "nested"
    sub.mkdir(parents=True)
    # two valid files, one in a nested directory, stable relative ordering
    burst = np.concatenate([
        rng.normal(0, 0.003, SR // 4),
        rng.normal(0, 0.3, SR // 2),
        rng.normal(0, 0.003, SR // 4),
    ]).astype(np.float32)
    _write_wav(root / "a_first.wav", burst)
    _write_wav(sub / "b_second.wav", burst)
    # truncated file: data chunk cut short while the frame header stays intact
    _write_wav(root / "c_broken.wav", rng.normal(0, 0.1, SR // 2).astype(np.float32),
               truncate_bytes=SR // 4)
    # non-WAV file must be ignored by the scan
    (root / "notes.txt").write_text("ignore me")
    return root


def test_read_wav_roundtrips_shape_and_meta(wav_dir: Path):
    audio = read_wav(str(wav_dir / "a_first.wav"))
    assert audio.sample_rate == SR
    assert audio.channels == 1
    assert audio.sample_width == 2
    assert audio.total_samples == SR
    assert audio.samples.shape == (SR, 1)
    assert np.all(np.abs(audio.samples) <= 1.0)


def test_truncated_file_raises_wav_error(wav_dir: Path):
    with pytest.raises(WavError, match="truncated"):
        read_wav(str(wav_dir / "c_broken.wav"))


def test_missing_and_non_pcm_rejected(tmp_path: Path, rng):
    missing = tmp_path / "nope.wav"
    with pytest.raises(WavError):
        read_wav(str(missing))
    compressed = tmp_path / "mu.wav"
    _write_wav(compressed, rng.normal(0, 0.1, 1000).astype(np.float32),
               non_pcm=True)
    with pytest.raises(WavError):
        read_wav(str(compressed))


def test_empty_wav_rejected(tmp_path: Path):
    path = tmp_path / "empty.wav"
    with wave.open(str(path), "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(SR)
    with pytest.raises(WavError, match="empty"):
        read_wav(str(path))


def test_scan_sorts_by_relative_path_and_skips_non_wav(wav_dir: Path):
    found = find_wav_files(wav_dir)
    rel = [p.relative_to(wav_dir).as_posix() for p in found]
    assert rel == sorted(rel)
    assert rel == ["a_first.wav", "c_broken.wav", "nested/b_second.wav"]


def test_batch_records_success_and_failure_without_aborting(
    wav_dir: Path, tmp_path: Path
):
    db = tmp_path / "r.sqlite"
    with Store(db) as store:
        summary = run_batch(wav_dir, store, DetectParams())
        assert summary.files_ok == 2
        assert summary.files_failed == 1
        assert summary.segment_count >= 2
        rows = store.conn.execute(
            "SELECT rel_path, status, error FROM files ORDER BY rel_path"
        ).fetchall()
    statuses = {r[0]: (r[1], r[2]) for r in rows}
    assert statuses["a_first.wav"][0] == "ok"
    assert statuses["nested/b_second.wav"][0] == "ok"
    assert statuses["c_broken.wav"][0] == "failed"
    assert "truncated" in statuses["c_broken.wav"][1]


def test_failed_file_does_not_pollute_successful_records(
    wav_dir: Path, tmp_path: Path
):
    db = tmp_path / "r.sqlite"
    with Store(db) as store:
        run_batch(wav_dir, store, DetectParams())
        # successful files own segments; the failed one owns none
        counts = dict(store.conn.execute(
            "SELECT f.rel_path, COUNT(s.id) FROM files f "
            "LEFT JOIN segments s ON s.file_id = f.id GROUP BY f.rel_path"
        ).fetchall())
    assert counts["a_first.wav"] >= 1
    assert counts["c_broken.wav"] == 0


def test_repeat_imports_are_distinct_runs_with_content_hash(
    wav_dir: Path, tmp_path: Path
):
    db = tmp_path / "r.sqlite"
    with Store(db) as store:
        first = run_batch(wav_dir, store, DetectParams(frame_ms=10))
        second = run_batch(wav_dir, store, DetectParams(frame_ms=20))
        assert first.run_id != second.run_id
        hashes = store.conn.execute(
            "SELECT DISTINCT content_sha256 FROM files WHERE rel_path = 'a_first.wav'"
        ).fetchall()
        assert len(hashes) == 1  # identical content -> identical hash
        param_sets = store.conn.execute(
            "SELECT DISTINCT params_json FROM runs"
        ).fetchall()
        assert len(param_sets) == 2  # different params -> different records


def test_export_matches_database_and_is_valid_json(
    wav_dir: Path, tmp_path: Path
):
    db = tmp_path / "r.sqlite"
    out = tmp_path / "out" / "segments.json"
    with Store(db) as store:
        summary = run_batch(wav_dir, store, DetectParams())
        target = export(store, summary.run_id, out)
    assert target == out and out.exists()
    payload = json.loads(out.read_text())
    assert payload["run_id"] == summary.run_id
    with Store(db) as store:
        for entry in payload["files"]:
            if entry["status"] != "ok":
                continue
            for seg in entry["segments"]:
                assert isinstance(seg["start_sample"], int)
                assert isinstance(seg["end_sample"], int)
                assert seg["end_sample"] > seg["start_sample"] >= 0


def test_csv_export_includes_segments_and_failures(wav_dir: Path, tmp_path: Path):
    db = tmp_path / "r.sqlite"
    out = tmp_path / "segments.csv"
    with Store(db) as store:
        summary = run_batch(wav_dir, store, DetectParams())
        export(store, summary.run_id, out)
    text = out.read_text()
    assert "segment" in text and "failed" in text and "c_broken.wav" in text


def test_export_unknown_run_raises(wav_dir: Path, tmp_path: Path):
    db = tmp_path / "r.sqlite"
    with Store(db) as store:
        run_batch(wav_dir, store, DetectParams())
        with pytest.raises(KeyError):
            store.load_run(999)


def test_atomic_export_leaves_no_temp_files(wav_dir: Path, tmp_path: Path):
    db = tmp_path / "r.sqlite"
    out = tmp_path / "segments.json"
    with Store(db) as store:
        summary = run_batch(wav_dir, store, DetectParams())
        export(store, summary.run_id, out)
    leftovers = [p.name for p in tmp_path.iterdir() if p.name.startswith(".")]
    assert leftovers == []


def test_cli_batch_exit_codes_and_summary(wav_dir: Path, tmp_path: Path,
                                          capsys: pytest.CaptureFixture):
    from audiosplit.cli import main

    db = tmp_path / "cli.sqlite"
    out = tmp_path / "segments.json"
    code = main([
        "batch", "--input", str(wav_dir), "--db", str(db),
        "--export", str(out), "--frame-ms", "10",
        "--noise-margin-db", "8", "--min-silence-ms", "120",
    ])
    assert code == 1  # one corrupt file -> partial failure
    summary = json.loads(capsys.readouterr().out)
    for field in ("files_ok", "files_failed", "segments", "silence_total_ms"):
        assert field in summary
    assert summary["files_ok"] == 2 and summary["files_failed"] == 1
    assert summary["segments"] >= 2

    # parameter errors map to exit code 2, not a batch failure
    bad = main(["batch", "--input", str(wav_dir), "--db", str(db),
                "--frame-ms", "0"])
    assert bad == 2
    missing = main(["batch", "--input", str(tmp_path / "ghost"),
                    "--db", str(db)])
    assert missing == 2


def test_cli_detect_single_file(wav_dir: Path, tmp_path: Path):
    from audiosplit.cli import main

    db = tmp_path / "single.sqlite"
    code = main(["detect", "--input", str(wav_dir / "a_first.wav"),
                 "--db", str(db)])
    assert code == 0
    code_bad = main(["detect", "--input", str(wav_dir / "c_broken.wav"),
                     "--db", str(db)])
    assert code_bad == 1
