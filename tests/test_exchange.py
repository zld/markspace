"""Tests for exchange formats, the OTIO adapter, and the renderer."""

from __future__ import annotations

import subprocess
import sys
import tempfile
import unittest
from fractions import Fraction
from pathlib import Path

from cutline.formats import v1, v2
from cutline.formats.common import FormatError
from cutline.model import Clip, Gap, MediaRef, Timeline, Track, validate
from cutline.otio_adapter import AdapterError, from_otio, to_otio
from cutline.render import render
from cutline.resolve import resolve
from cutline.sample import make_sample


def sample_clips_equal(a: Timeline, b: Timeline) -> None:
    assert a.rate == b.rate
    assert len(a.tracks) == len(b.tracks)
    for ta, tb in zip(a.tracks, b.tracks):
        assert (ta.name, ta.kind, ta.mode) == (tb.name, tb.kind, tb.mode)
        assert len(ta.items) == len(tb.items)
        for ia, ib in zip(ta.items, tb.items):
            assert type(ia) is type(ib)
            assert (ia.start, ia.duration) == (ib.start, ib.duration)
            if isinstance(ia, Clip):
                assert ia.clip_id == ib.clip_id
                assert (ia.source_in, ia.source_out) == (ib.source_in, ib.source_out)
                assert ia.priority == ib.priority
                assert ia.media.name == ib.media.name
                assert ia.media.uri == ib.media.uri
                assert ia.media.rate == ib.media.rate
                assert ia.media.duration == ib.media.duration


class TestV1(unittest.TestCase):
    def test_round_trip_preserves_everything(self):
        tl = make_sample(seed=41)
        doc, err = v1.dumps(tl)
        self.assertEqual(err, 0)
        sample_clips_equal(tl, v1.loads(doc))

    def test_non_integer_rate_rejected(self):
        tl = make_sample()
        tl.rate = Fraction(24000, 1001)
        with self.assertRaisesRegex(FormatError, "non-integer rate"):
            v1.dumps(tl)

    def test_non_integer_rate_quantized_with_error_report(self):
        tl = make_sample()
        tl.rate = Fraction(24000, 1001)
        doc, max_error = v1.dumps(tl, quantize=True)
        self.assertEqual(doc["rate"], 24)
        self.assertEqual(max_error, 0)

    def test_fractional_frames_require_quantize(self):
        tl = make_sample()
        clip = tl.tracks[0].items[0]
        tl.tracks[0].items[0] = clip.moved_to(Fraction(1, 2))
        with self.assertRaisesRegex(FormatError, "not an integer"):
            v1.dumps(tl)
        doc, max_error = v1.dumps(tl, quantize=True)
        self.assertEqual(max_error, Fraction(1, 2))

    def test_missing_key_diagnostic(self):
        with self.assertRaisesRegex(FormatError, "missing required key 'rate'"):
            v1.loads({"version": 1, "tracks": []})

    def test_oversized_input_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            big = Path(tmp) / "big.json"
            big.write_bytes(b" " * (10 * 1024 * 1024 + 1))
            with self.assertRaisesRegex(FormatError, "exceeds"):
                v1.load(big)


class TestV2(unittest.TestCase):
    def test_round_trip_non_integer_rate(self):
        tl = make_sample(seed=7)
        tl.rate = Fraction(24000, 1001)
        out = v2.loads(v2.dumps(tl))
        self.assertEqual(out.rate, Fraction(24000, 1001))
        sample_clips_equal(tl, out)

    def test_half_open_ranges_and_modes(self):
        doc = v2.dumps(make_sample())
        modes = {t["name"]: t["mode"] for t in doc["tracks"]}
        self.assertEqual(modes, {"video": "exclusive", "audio": "layered"})
        first = doc["tracks"][0]["items"][0]
        self.assertEqual(first["range"], {"start": 0, "end": 100})

    def test_bad_range_rejected(self):
        doc = v2.dumps(make_sample())
        doc["tracks"][0]["items"][0]["range"] = {"start": 100, "end": 50}
        with self.assertRaisesRegex(FormatError, "end 50 < start 100"):
            v2.loads(doc)


class TestOtioAdapter(unittest.TestCase):
    def test_round_trip_lossless(self):
        tl = resolve(make_sample(seed=99)).timeline
        out = from_otio(to_otio(tl))
        sample_clips_equal(tl, out)

    def test_round_trip_with_gaps_and_fractional_times(self):
        media = MediaRef(name="m.mov", rate=Fraction(24), duration=Fraction(1000))
        clip = Clip(clip_id="c", name="c", start=Fraction(10),
                    duration=Fraction(25, 2), source_in=Fraction(3),
                    source_out=Fraction(31, 2), media=media, priority=2)
        tl = Timeline(name="g", rate=Fraction(24), tracks=[Track(
            name="v", kind="video", mode="exclusive",
            items=[Gap(Fraction(0), Fraction(10)), clip])])
        out = from_otio(to_otio(tl))
        sample_clips_equal(tl, out)

    def test_missing_rate_metadata_errors_with_identity(self):
        import opentimelineio as otio
        tl = otio.schema.Timeline(name="mystery")
        with self.assertRaisesRegex(AdapterError, "mystery"):
            from_otio(tl)

    def test_unsupported_object_errors_with_identity(self):
        import opentimelineio as otio
        tl = to_otio(resolve(make_sample()).timeline)
        tl.tracks.append(otio.schema.Stack(name="odd"))
        with self.assertRaisesRegex(AdapterError, "Stack.*odd"):
            from_otio(tl)

    def test_unresolved_exclusive_overlap_rejected(self):
        with self.assertRaisesRegex(AdapterError, "overlaps"):
            to_otio(make_sample())


class TestRender(unittest.TestCase):
    def test_deterministic_bytes(self):
        tl = make_sample(seed=41)
        self.assertEqual(render(tl, 320), render(tl, 320))

    def test_deterministic_across_processes(self):
        import hashlib
        here = Path(__file__).resolve().parents[1]
        code = (
            "import hashlib, sys;"
            "from cutline.sample import make_sample;"
            "from cutline.render import render;"
            "print(hashlib.sha256(render(make_sample(41), 640)).hexdigest())"
        )
        env = {"PYTHONPATH": str(here / "src"), "PATH": "/usr/bin:/bin"}
        hashes = {
            subprocess.run([sys.executable, "-c", code], env=env,
                           capture_output=True, text=True, check=True).stdout.strip()
            for _ in range(2)
        }
        self.assertEqual(len(hashes), 1)
        self.assertEqual(len(hashes.pop()), 64)


if __name__ == "__main__":
    unittest.main()
