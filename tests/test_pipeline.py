"""End-to-end pipeline tests: parsing, comparison, packaging, recovery."""
from __future__ import annotations

import json
import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "scripts"))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "src"))

import make_fixtures  # noqa: E402
from trackaudit import boxes, cli, compare, extract, pack, store  # noqa: E402


class PipelineTestCase(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls._tmp = tempfile.TemporaryDirectory()
        cls.root = Path(cls._tmp.name)
        cls.media = cls.root / "media"
        make_fixtures.build_all(cls.media)

    @classmethod
    def tearDownClass(cls):
        cls._tmp.cleanup()

    def path(self, name):
        return self.media / name


class TestBoxes(PipelineTestCase):
    def test_extended_size_and_zero_size_boxes(self):
        master = boxes.read_box_tree(self.path("master.mp4"))
        mdat = next(b for b in master if b.type == "mdat")
        self.assertEqual(mdat.header_size, boxes.LARGE_HEADER_SIZE)
        reordered = boxes.read_box_tree(self.path("reordered.mp4"))
        mdat = next(b for b in reordered if b.type == "mdat")
        self.assertEqual(mdat.end, self.path("reordered.mp4").stat().st_size)

    def test_nested_structure_found(self):
        tree = boxes.read_box_tree(self.path("master.mp4"))
        moov = next(b for b in tree if b.type == "moov")
        self.assertEqual(len(moov.find("trak")), 4)
        trak = moov.find("trak")[0]
        stbl = trak.first("mdia").first("minf").first("stbl")
        self.assertIsNotNone(stbl.first("stsd"))
        self.assertIsNotNone(stbl.first("stts"))

    def test_truncated_reports_offset_and_category(self):
        with self.assertRaises(boxes.BoxError) as ctx:
            boxes.read_box_tree(self.path("truncated.mp4"))
        self.assertEqual(ctx.exception.category, boxes.ERR_TRUNCATED)
        self.assertEqual(ctx.exception.offset, 24)

    def test_invalid_size_rejected(self):
        bad = self.root / "badsize.mp4"
        bad.write_bytes(make_fixtures.make_ftyp()
                        + (4).to_bytes(4, "big") + b"free")
        with self.assertRaises(boxes.BoxError) as ctx:
            boxes.read_box_tree(bad)
        self.assertEqual(ctx.exception.category, boxes.ERR_INVALID_SIZE)

    def test_not_mp4_rejected(self):
        bad = self.root / "plain.mp4"
        bad.write_bytes(b"\0" * 64)
        with self.assertRaises(boxes.BoxError) as ctx:
            boxes.read_box_tree(bad)
        self.assertEqual(ctx.exception.category, boxes.ERR_NOT_MP4)


class TestExtract(PipelineTestCase):
    def test_master_tracks(self):
        info = extract.extract(self.path("master.mp4"))
        self.assertTrue(info.ok)
        self.assertEqual(len(info.tracks), 4)
        by_id = {t.track_id: t for t in info.tracks}
        video = by_id[1]
        self.assertEqual((video.media_type, video.codec), ("video", "avc1"))
        self.assertEqual((video.width, video.height), (1920, 1080))
        self.assertEqual(video.duration_ms, 4000.0)
        audio = by_id[2]
        self.assertEqual((audio.language, audio.sample_rate,
                          audio.channels), ("eng", 48000, 2))
        self.assertEqual(by_id[3].language, "zho")
        self.assertEqual(by_id[4].media_type, "subtitle")
        self.assertEqual(info.duration_ms, 4000.0)
        self.assertIn("avc1 1920x1080", info.codec_summary)
        self.assertEqual(info.unknown_boxes, {"xtra": 1})
        self.assertEqual(audio.sources["language"], "mdia.mdhd")

    def test_truncated_captured_not_raised(self):
        info = extract.extract(self.path("truncated.mp4"))
        self.assertFalse(info.ok)
        self.assertEqual(info.errors[0]["category"], boxes.ERR_TRUNCATED)


class TestCompare(PipelineTestCase):
    def setUp(self):
        self.master = extract.extract(self.path("master.mp4"))

    def test_reorder_tolerated(self):
        reordered = extract.extract(self.path("reordered.mp4"))
        mismatches = compare.compare_to_reference(self.master, reordered,
                                                  40.0)
        self.assertEqual(mismatches, [])
        self.assertEqual(compare.check_internal(reordered, 40.0), [])

    def test_drift_and_missing_track_detected(self):
        drift = extract.extract(self.path("drift.mp4"))
        mismatches = compare.check_internal(drift, 40.0)
        self.assertEqual(len(mismatches), 1)
        self.assertEqual(mismatches[0].kind, "duration_delta")
        self.assertEqual(mismatches[0].actual, 4250.0)
        cross = compare.compare_to_reference(self.master, drift, 40.0)
        self.assertEqual([m.kind for m in cross], ["track_missing"])
        self.assertEqual(cross[0].track_id, 3)

    def test_duplicate_track_id_flagged(self):
        doubled = extract.extract(self.path("master.mp4"))
        doubled.tracks.append(doubled.tracks[0])
        kinds = [m.kind for m in compare.check_internal(doubled, 40.0)]
        self.assertIn("duplicate_track_id", kinds)


class TestPack(PipelineTestCase):
    def test_pack_success(self):
        results = [extract.extract(self.path(n))
                   for n in ("master.mp4", "reordered.mp4")]
        out = self.root / "pkg-ok"
        manifest = pack.build_package(results, {}, out, pack.PackPolicy())
        self.assertEqual(len(manifest["files"]), 2)
        self.assertTrue((out / "manifest.json").is_file())
        self.assertTrue((out / "media" / "master.mp4").is_file())

    def test_pack_failure_leaves_nothing(self):
        results = [extract.extract(self.path(n))
                   for n in ("master.mp4", "truncated.mp4")]
        out = self.root / "pkg-bad"
        with self.assertRaises(pack.PackError) as ctx:
            pack.build_package(results, {}, out, pack.PackPolicy())
        self.assertEqual(ctx.exception.exit_code, 2)
        self.assertFalse(out.exists())
        leftovers = [p for p in self.root.iterdir()
                     if p.name.startswith(".trackaudit-pack-")]
        self.assertEqual(leftovers, [])


class TestStoreAndCli(PipelineTestCase):
    def test_store_reuse_and_version_invalidation(self):
        db = self.root / "audit.sqlite"
        info = extract.extract(self.path("master.mp4"))
        with store.Store(db) as s:
            s.save(info.sha256, info.path, extract.EXTRACTOR_VERSION,
                   info.to_dict())
            hit = s.lookup(info.sha256, extract.EXTRACTOR_VERSION)
            self.assertIsNotNone(hit)
            self.assertEqual(hit["codec_summary"], info.codec_summary)
            self.assertIsNone(s.lookup(info.sha256,
                                       extract.EXTRACTOR_VERSION + 1))

    def test_cli_inspect_exit_codes_and_cache(self):
        db = self.root / "cli.sqlite"
        json_path = self.root / "report.json"
        code = cli.main(["inspect", str(self.media), "--json",
                         str(json_path), "--db", str(db)])
        self.assertEqual(code, 2)  # truncated.mp4 is damaged
        report = json.loads(json_path.read_text())
        self.assertEqual(report["summary"]["files"], 4)
        self.assertEqual(report["summary"]["tracks"], 11)
        self.assertEqual(report["summary"]["duration_delta_ms"], 250.0)
        for key in ("files", "errors"):
            self.assertIn(key, report)
        for key in ("tracks", "duration_ms", "codec", "mismatches",
                    "errors"):
            self.assertIn(key, report["files"][0])
        with sqlite3.connect(db) as conn:
            self.assertEqual(conn.execute(
                "SELECT COUNT(*) FROM audits").fetchone()[0], 4)
        # Second run reuses cached results.
        code = cli.main(["inspect", str(self.media), "--db", str(db)])
        self.assertEqual(code, 2)
        # Clean subset: reordered tracks are tolerated, exit 0.
        code = cli.main(["inspect", str(self.path("master.mp4")),
                         str(self.path("reordered.mp4"))])
        self.assertEqual(code, 0)
        # Drift file alone: internal inconsistency, exit 1.
        code = cli.main(["inspect", str(self.path("drift.mp4"))])
        self.assertEqual(code, 1)
        # Missing input path: argument failure, exit 3.
        code = cli.main(["inspect", str(self.root / "nope.mp4")])
        self.assertEqual(code, 3)

    def test_cli_symlink_rejected(self):
        link = self.root / "link.mp4"
        link.symlink_to(self.path("master.mp4"))
        code = cli.main(["inspect", str(link)])
        self.assertEqual(code, 2)

    def test_cli_pack_atomicity(self):
        out = self.root / "cli-pkg"
        code = cli.main(["pack", str(self.media), "--out", str(out)])
        self.assertEqual(code, 2)
        self.assertFalse(out.exists())
        code = cli.main(["pack", str(self.path("master.mp4")),
                         str(self.path("reordered.mp4")),
                         "--out", str(out)])
        self.assertEqual(code, 0)
        manifest = json.loads((out / "manifest.json").read_text())
        self.assertEqual(len(manifest["files"]), 2)


if __name__ == "__main__":
    unittest.main()
