"""End-to-end pipeline tests: clean run, fault detection, corrupt inputs,
package refusal paths."""
from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

from medialint import fixtures
from medialint.__main__ import main
from medialint.mp4 import parse_mp4
from medialint.model import MediaError


class PipelineTestCase(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(self._tmp.name)
        self.db = self.root / "catalog.sqlite"

    def tearDown(self):
        self._tmp.cleanup()

    def _run(self, *argv) -> int:
        return main(list(argv))

    def _make_bundle(self, name: str, inject: str = "") -> Path:
        out = self.root / name
        rc = self._run("fixtures", "--out", str(out), "--inject", inject)
        self.assertEqual(rc, 0)
        return out / "bundle.json"

    def _audit(self, manifest: Path, report: Path) -> int:
        return self._run("audit", "--manifest", str(manifest), "--db", str(self.db),
                         "--json", str(report), "--tolerance-ms", "80")

    def test_clean_pipeline_and_package(self):
        manifest = self._make_bundle("clean")
        report = self.root / "report.json"
        self.assertEqual(self._audit(manifest, report), 0)
        doc = json.loads(report.read_text())
        self.assertEqual(doc["track_count"], 4)
        self.assertEqual(doc["max_duration_delta_ms"], 0)
        self.assertEqual(doc["inconsistencies"], [])
        self.assertIn("H.264 1920x1080", doc["codec_summary"])
        self.assertIn("AAC 48000Hz/2ch", doc["codec_summary"])
        pkg = self.root / "pkg"
        rc = self._run("package", "--manifest", str(manifest), "--db", str(self.db), "--out", str(pkg))
        self.assertEqual(rc, 0)
        package_doc = json.loads((pkg / "package.json").read_text())
        self.assertEqual(len(package_doc["assets"]), 2)
        self.assertTrue((pkg / "media/intro.mp4").is_file())
        self.assertTrue((pkg / "media/voiceover.wav").is_file())

    def test_faulty_fixtures_are_reported(self):
        manifest = self._make_bundle("faulty", inject="duration,aac-rate,truncated")
        report = self.root / "report.json"
        self.assertEqual(self._audit(manifest, report), 2)
        doc = json.loads(report.read_text())
        codes = {i["code"] for i in doc["inconsistencies"]}
        self.assertEqual(codes, {"duration-delta", "codec-param-mismatch", "truncated-chunk"})
        duration = next(i for i in doc["inconsistencies"] if i["code"] == "duration-delta")
        self.assertEqual(duration["detail"]["delta_ms"], 300)
        rate = next(i for i in doc["inconsistencies"] if i["code"] == "codec-param-mismatch")
        self.assertEqual(rate["detail"]["sample_rate"], {"expected": 48000, "actual": 44100})
        trunc = next(i for i in doc["inconsistencies"] if i["code"] == "truncated-chunk")
        self.assertEqual(trunc["path"], "media/voiceover.wav")
        self.assertIsNotNone(trunc["offset"])

    def test_unrecognized_and_truncated_mp4(self):
        bundle = self.root / "corrupt"
        (bundle / "media").mkdir(parents=True)
        (bundle / "media/garbage.bin").write_bytes(b"NOPE not a container at all")
        intro = fixtures.build_intro_mp4(set())
        (bundle / "media/cut.mp4").write_bytes(intro[: len(intro) * 3 // 5])
        manifest = {
            "bundle": "corrupt",
            "assets": [
                {"path": "media/garbage.bin", "tracks": [
                    {"type": "audio", "codec": "pcm", "duration_ms": 1}]},
                {"path": "media/cut.mp4", "tracks": [
                    {"type": "video", "codec": "avc1", "duration_ms": 4000}]},
            ],
        }
        mpath = bundle / "bundle.json"
        mpath.write_text(json.dumps(manifest))
        report = self.root / "report.json"
        self.assertEqual(self._audit(mpath, report), 2)
        doc = json.loads(report.read_text())
        codes = {i["code"] for i in doc["inconsistencies"]}
        self.assertIn("unrecognized-format", codes)
        self.assertIn("truncated-box", codes)

    def test_package_refuses_changed_media(self):
        manifest = self._make_bundle("clean")
        self.assertEqual(self._audit(manifest, self.root / "report.json"), 0)
        wav = manifest.parent / "media/voiceover.wav"
        wav.write_bytes(wav.read_bytes() + b"\0" * 16)  # change after the scan
        rc = self._run("package", "--manifest", str(manifest), "--db", str(self.db),
                       "--out", str(self.root / "pkg"))
        self.assertEqual(rc, 3)
        self.assertFalse((self.root / "pkg/package.json").exists())

    def test_package_refuses_inconsistent_assets(self):
        manifest = self._make_bundle("faulty", inject="duration")
        self.assertEqual(self._audit(manifest, self.root / "report.json"), 2)
        rc = self._run("package", "--manifest", str(manifest), "--db", str(self.db),
                       "--out", str(self.root / "pkg"))
        self.assertEqual(rc, 2)
        self.assertFalse((self.root / "pkg/package.json").exists())

    def test_path_escape_is_rejected(self):
        bundle = self.root / "escape"
        bundle.mkdir()
        manifest = {"bundle": "x", "assets": [
            {"path": "../outside.mp4", "tracks": [
                {"type": "video", "codec": "avc1", "duration_ms": 1}]}]}
        mpath = bundle / "bundle.json"
        mpath.write_text(json.dumps(manifest))
        self.assertEqual(self._audit(mpath, self.root / "report.json"), 3)

    def test_bad_manifest_is_exit_3(self):
        mpath = self.root / "broken.json"
        mpath.write_text("{not json")
        self.assertEqual(self._audit(mpath, self.root / "report.json"), 3)


class Mp4ParserTestCase(unittest.TestCase):
    def _wrap(self, moov_body: bytes) -> bytes:
        ftyp = fixtures._box(b"ftyp", b"isom" + b"\0" * 4 + b"isom")
        return ftyp + fixtures._box(b"moov", moov_body)

    def test_duplicate_track_id(self):
        moov = fixtures._mvhd(1000, 4000, 4)
        moov += fixtures._audio_trak(2, "eng", 48000, 4000)
        moov += fixtures._audio_trak(2, "zho", 48000, 4000)
        tracks, problems = parse_mp4(self._wrap(moov), "dup.mp4")
        self.assertEqual(len(tracks), 2)
        self.assertTrue(any(p.code == "duplicate-track-id" for p in problems))

    def test_unsupported_codec_entry(self):
        mdia = fixtures._mdhd(90000, 360000, "und") + fixtures._hdlr(b"vide", b"V")
        minf = (fixtures._fullbox(b"vmhd", 0, 1, b"\0" * 8) + fixtures._dinf()
                + fixtures._stbl(fixtures._box(b"hvc1", b"\0" * 78)))
        trak = fixtures._box(b"trak", fixtures._tkhd(1, 4000, 1920, 1080)
                             + fixtures._box(b"mdia", mdia + fixtures._box(b"minf", minf)))
        tracks, problems = parse_mp4(self._wrap(fixtures._mvhd(1000, 4000, 2) + trak), "hvc1.mp4")
        self.assertTrue(any(p.code == "unsupported-codec" for p in problems))

    def test_fragmented_mp4_rejected(self):
        data = fixtures._box(b"ftyp", b"isom" + b"\0" * 4 + b"isom")
        data += fixtures._box(b"moof", b"\0" * 16) + fixtures._box(b"moov", fixtures._mvhd(1000, 1, 2))
        with self.assertRaises(MediaError) as ctx:
            parse_mp4(data, "frag.mp4")
        self.assertEqual(ctx.exception.code, "unsupported-container")


if __name__ == "__main__":
    unittest.main()
