"""Snapshot store, comparison detail and CLI batch tests."""
from __future__ import annotations

import json
import sys
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "src"))
sys.path.insert(0, str(ROOT / "scripts"))

from make_samples import BASE_TRACKS, build_container, write_samples  # noqa: E402
from trackcheck import cli  # noqa: E402
from trackcheck.consistency import compare_media  # noqa: E402
from trackcheck.probe import probe_file  # noqa: E402
from trackcheck.store import SnapshotStore, StoreError  # noqa: E402


@pytest.fixture()
def samples(tmp_path: Path) -> dict[str, Path]:
    return write_samples(tmp_path / "samples", seed=17)


def test_store_cache_hit_and_rebuild(samples: dict[str, Path],
                                     tmp_path: Path) -> None:
    db = tmp_path / "store.sqlite"
    store = SnapshotStore(db)
    media = probe_file(samples["base.pwm"])
    stat = samples["base.pwm"].stat()
    assert store.get(samples["base.pwm"], media.content_hash) is None
    store.put(media, stat.st_mtime_ns, stat.st_size)
    cached = store.get(samples["base.pwm"], media.content_hash)
    assert cached is not None
    assert cached.content_hash == media.content_hash
    assert [t.track_id for t in cached.tracks] == [1, 2, 3]
    # Wrong hash never returns stale data.
    assert store.get(samples["base.pwm"], "deadbeef") is None
    store.rebuild()
    assert store.get(samples["base.pwm"], media.content_hash) is None
    store.close()


def test_corrupt_store_raises_and_rebuild_recovers(tmp_path: Path) -> None:
    db = tmp_path / "broken.sqlite"
    db.write_text("not a database")
    with pytest.raises(StoreError):
        SnapshotStore(db)
    rebuilt = SnapshotStore(db, rebuild=True)
    rebuilt.close()
    assert db.exists()


def test_compare_reports_missing_extra_renamed_and_order(
        tmp_path: Path) -> None:
    base_path = tmp_path / "base.pwm"
    base_path.write_bytes(build_container(BASE_TRACKS, 1))
    # Reordered ids, renamed subtitle language.
    reordered = [BASE_TRACKS[2], BASE_TRACKS[0], BASE_TRACKS[1]]
    reordered = [
        (tid, k, c, tb, st, sc, w, h, sr, ch,
         ("fra" if tid == 3 else lang), f)
        for (tid, k, c, tb, st, sc, w, h, sr, ch, lang, f) in reordered
    ]
    other_path = tmp_path / "other.pwm"
    other_path.write_bytes(build_container(reordered, 2))
    # Add an extra track by shifting ids on a second variant.
    extra = list(BASE_TRACKS) + [
        (4, 3, "SRT ", 1000, 0, 500, 0, 0, 0, 0, "deu", 0)
    ]
    extra_path = tmp_path / "extra.pwm"
    extra_path.write_bytes(build_container(extra, 3))

    base, other, extra_media = (probe_file(base_path), probe_file(other_path),
                                probe_file(extra_path))
    result = compare_media(base, other, tolerance_ms=5.0)
    kinds = {m.kind for m in result.mismatches}
    assert {"order_changed", "renamed"} <= kinds

    extra_result = compare_media(base, extra_media, tolerance_ms=5.0)
    assert any(m.kind == "extra_track" and m.track_id == 4
               for m in extra_result.mismatches)

    missing_result = compare_media(extra_media, base, tolerance_ms=5.0)
    assert any(m.kind == "missing_track" and m.track_id == 4
               for m in missing_result.mismatches)


def test_tolerance_boundary(tmp_path: Path) -> None:
    base_path = tmp_path / "base.pwm"
    base_path.write_bytes(build_container(BASE_TRACKS, 1))
    # Subtitle: 1000 -> 1001 samples at timebase 1000 => exactly 1 ms drift.
    drifted = [
        (tid, k, c, tb, st, (sc + 1 if tid == 3 else sc), w, h, sr, ch,
         lang, f)
        for (tid, k, c, tb, st, sc, w, h, sr, ch, lang, f) in BASE_TRACKS
    ]
    drift_path = tmp_path / "drift.pwm"
    drift_path.write_bytes(build_container(drifted, 2))
    base, drift = probe_file(base_path), probe_file(drift_path)
    assert compare_media(base, drift, tolerance_ms=1.0).ok
    assert not compare_media(base, drift, tolerance_ms=0.9).ok


def test_cli_batch_continues_after_bad_file(samples: dict[str, Path],
                                            tmp_path: Path,
                                            capsys: pytest.CaptureFixture[str]
                                            ) -> None:
    missing = tmp_path / "nope.pwm"
    code = cli.main([
        "scan", str(samples["base.pwm"]), str(samples["badversion.pwm"]),
        str(missing),
    ])
    out = capsys.readouterr()
    assert code == cli.EXIT_INPUT
    assert "status=ok" in out.out
    assert "E_VERSION" in out.out
    assert "cannot read" in out.err


def test_cli_unwritable_json_is_exit_3(samples: dict[str, Path],
                                       capsys: pytest.CaptureFixture[str]
                                       ) -> None:
    code = cli.main([
        "compare", "--baseline", str(samples["base.pwm"]),
        "--input", str(samples["base.pwm"]),
        "--json", "/nonexistent-dir-xyz/report.json",
    ])
    assert code == cli.EXIT_STORAGE
    assert "cannot write" in capsys.readouterr().err


def test_report_sorting_is_stable(samples: dict[str, Path],
                                  tmp_path: Path) -> None:
    report = tmp_path / "r.json"
    cli.main([
        "compare", "--baseline", str(samples["base.pwm"]),
        "--input", str(samples["drift.pwm"]), "--json", str(report),
        "--tolerance-ms", "1",
    ])
    text = report.read_text()
    payload = json.loads(text)
    assert text == json.dumps(payload, indent=2, sort_keys=True) + "\n"
    ids = [c["track_id"] for c in payload["codec_summary"]["input"]]
    assert ids == sorted(ids)
