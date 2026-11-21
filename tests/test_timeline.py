"""Tests for time boundaries, blending, validation, and export digests.

Expected pixels are computed independently of the compositor (plain Python
integer arithmetic), so the exporter is never verified against itself.
"""

from __future__ import annotations

import hashlib
import json
import tempfile
import unittest
from fractions import Fraction
from pathlib import Path

import numpy as np
from PIL import Image

from timeline_render.blend import composite, unpremultiply
from timeline_render.compose import compose_frame, load_images, opacity_at, opacity_byte
from timeline_render.export import audio_alignment_error, render_summary
from timeline_render.model import (
    AudioEvent,
    FrameRate,
    InputError,
    Keyframe,
    Layer,
    Timeline,
)
from timeline_render.validate import load_timeline

W, H = 2, 2


def make_layer(**kw) -> Layer:
    base = dict(
        name="l", png="l.png", start_frame=0, end_frame=2,
        opacity=Fraction(1), keyframes=(), blend="normal", z=0, order=0,
    )
    base.update(kw)
    return Layer(**base)


def make_timeline(layers=(), events=(), sample_rate=0, frame_count=2) -> Timeline:
    return Timeline(
        frame_rate=FrameRate(2, 1),
        frame_count=frame_count,
        layers=tuple(layers),
        sample_rate=sample_rate,
        events=tuple(events),
        base_dir=Path("."),
    )


def solid_image(rgba) -> np.ndarray:
    img = np.zeros((H, W, 4), dtype=np.uint8)
    img[...] = rgba
    return img


class BlendTests(unittest.TestCase):
    def test_normal_over_opaque(self):
        # Independent expectation: A=128, out = (src*128 + dst*127) // 255.
        canvas_p = np.zeros((1, 1, 3), dtype=np.uint16)
        canvas_a = np.full((1, 1), 255, dtype=np.uint16)
        canvas_p[0, 0] = [0, 0, 255]  # premultiplied opaque blue
        src = np.full((1, 1, 3), 0, dtype=np.uint16)
        src[0, 0] = [255, 0, 0]
        alpha = np.full((1, 1), 128, dtype=np.uint16)
        p, a = composite(canvas_p, canvas_a, src, alpha, "normal")
        rgba = unpremultiply(p, a)
        exp = [(255 * 128 + 0 * 127) // 255, 0, (0 * 128 + 255 * 127) // 255, 255]
        self.assertEqual(rgba[0, 0].tolist(), exp)
        self.assertEqual(exp, [128, 0, 127, 255])

    def test_add_clamps(self):
        canvas_p = np.full((1, 1, 3), 200, dtype=np.uint16)
        canvas_a = np.full((1, 1), 255, dtype=np.uint16)
        src = np.full((1, 1, 3), 255, dtype=np.uint16)
        alpha = np.full((1, 1), 153, dtype=np.uint16)  # P = 153 per channel
        p, a = composite(canvas_p, canvas_a, src, alpha, "add")
        rgba = unpremultiply(p, a)
        self.assertEqual(rgba[0, 0].tolist(), [255, 255, 255, 255])

    def test_multiply(self):
        canvas_p = np.zeros((1, 1, 3), dtype=np.uint16)
        canvas_p[0, 0] = [128, 255, 64]
        canvas_a = np.full((1, 1), 255, dtype=np.uint16)
        src = np.zeros((1, 1, 3), dtype=np.uint16)
        src[0, 0] = [255, 128, 255]
        alpha = np.full((1, 1), 255, dtype=np.uint16)
        p, a = composite(canvas_p, canvas_a, src, alpha, "multiply")
        rgba = unpremultiply(p, a)
        exp = [128 * 255 // 255, 255 * 128 // 255, 64 * 255 // 255, 255]
        self.assertEqual(rgba[0, 0].tolist(), exp)

    def test_transparent_rgb_does_not_leak(self):
        # Fully transparent source with garbage RGB must not alter the canvas.
        canvas_p = np.zeros((1, 1, 3), dtype=np.uint16)
        canvas_a = np.zeros((1, 1), dtype=np.uint16)
        src = np.full((1, 1, 3), 255, dtype=np.uint16)
        alpha = np.zeros((1, 1), dtype=np.uint16)
        p, a = composite(canvas_p, canvas_a, src, alpha, "normal")
        rgba = unpremultiply(p, a)
        self.assertEqual(rgba[0, 0].tolist(), [0, 0, 0, 0])


class TimeBoundaryTests(unittest.TestCase):
    def test_half_open_interval(self):
        layer = make_layer(start_frame=0, end_frame=1)
        self.assertTrue(layer.start_frame <= 0 < layer.end_frame)
        self.assertFalse(layer.start_frame <= 1 < layer.end_frame)

    def test_keyframe_interpolation_and_clamp(self):
        kfs = (Keyframe(2, Fraction(0)), Keyframe(6, Fraction(1)))
        layer = make_layer(keyframes=kfs)
        self.assertEqual(opacity_at(layer, 0), Fraction(0))  # clamped low
        self.assertEqual(opacity_at(layer, 9), Fraction(1))  # clamped high
        self.assertEqual(opacity_at(layer, 4), Fraction(1, 2))  # midpoint
        self.assertEqual(opacity_at(layer, 3), Fraction(1, 4))

    def test_opacity_byte_rounds_half_up(self):
        layer = make_layer(opacity=Fraction(1, 2))
        self.assertEqual(opacity_byte(layer, 0), 128)
        self.assertEqual(opacity_byte(make_layer(opacity=Fraction(0)), 0), 0)
        self.assertEqual(opacity_byte(make_layer(opacity=Fraction(1)), 0), 255)


class ValidationTests(unittest.TestCase):
    def _write(self, tmp: Path, doc: dict) -> Path:
        p = tmp / "timeline.json"
        p.write_text(json.dumps(doc), encoding="utf-8")
        return p

    def _base_doc(self) -> dict:
        return {
            "frame_rate": {"num": 2, "den": 1},
            "duration": {"num": 1, "den": 1},
            "layers": [
                {"name": "a", "png": "a.png", "start_frame": 0, "end_frame": 2}
            ],
        }

    def test_unknown_blend_mode(self):
        with tempfile.TemporaryDirectory() as td:
            doc = self._base_doc()
            doc["layers"][0]["blend"] = "screen"
            with self.assertRaises(InputError) as ctx:
                load_timeline(self._write(Path(td), doc), W, H)
            self.assertIn("layers[0].blend", str(ctx.exception))

    def test_duplicate_keyframe(self):
        with tempfile.TemporaryDirectory() as td:
            doc = self._base_doc()
            doc["layers"][0]["keyframes"] = [
                {"frame": 0, "value": 0.5}, {"frame": 0, "value": 0.7}
            ]
            with self.assertRaises(InputError) as ctx:
                load_timeline(self._write(Path(td), doc), W, H)
            self.assertIn("keyframes", str(ctx.exception))

    def test_event_out_of_range(self):
        with tempfile.TemporaryDirectory() as td:
            doc = self._base_doc()
            doc["audio"] = {"sample_rate": 48000,
                            "events": [{"name": "hit", "sample": 96001}]}
            with self.assertRaises(InputError) as ctx:
                load_timeline(self._write(Path(td), doc), W, H)
            self.assertIn("audio.events[0].sample", str(ctx.exception))

    def test_layer_beyond_frame_count(self):
        with tempfile.TemporaryDirectory() as td:
            doc = self._base_doc()
            doc["layers"][0]["end_frame"] = 3
            with self.assertRaises(InputError) as ctx:
                load_timeline(self._write(Path(td), doc), W, H)
            self.assertIn("layers[0].end_frame", str(ctx.exception))

    def test_non_integral_frame_count(self):
        with tempfile.TemporaryDirectory() as td:
            doc = self._base_doc()
            doc["duration"] = {"num": 3, "den": 4}  # 1.5 frames at 2 fps
            with self.assertRaises(InputError):
                load_timeline(self._write(Path(td), doc), W, H)

    def test_missing_image_and_size_mismatch(self):
        with tempfile.TemporaryDirectory() as td:
            tmp = Path(td)
            layer = make_layer(name="a", png="missing.png")
            tl = make_timeline(layers=[layer])
            tl = Timeline(**{**tl.__dict__, "base_dir": tmp})
            with self.assertRaises(InputError) as ctx:
                load_images(tl, W, H)
            self.assertIn("layers[0].png", str(ctx.exception))
            Image.new("RGBA", (3, 3)).save(tmp / "big.png")
            layer2 = make_layer(name="b", png="big.png")
            tl2 = Timeline(**{**tl.__dict__, "layers": (layer2,)})
            with self.assertRaises(InputError) as ctx:
                load_images(tl2, W, H)
            self.assertIn("does not match", str(ctx.exception))


class ExportTests(unittest.TestCase):
    def test_digest_matches_independent_pixels(self):
        with tempfile.TemporaryDirectory() as td:
            tmp = Path(td)
            Image.new("RGBA", (W, H), (0, 0, 255, 255)).save(tmp / "blue.png")
            Image.new("RGBA", (W, H), (255, 0, 0, 255)).save(tmp / "red.png")
            layers = [
                make_layer(name="bg", png="blue.png", z=0),
                make_layer(name="fg", png="red.png", z=1,
                           opacity=Fraction(1, 2)),
            ]
            events = [AudioEvent("a", 0), AudioEvent("b", 24000)]
            tl = Timeline(
                frame_rate=FrameRate(2, 1), frame_count=2, layers=tuple(layers),
                sample_rate=48000, events=tuple(events), base_dir=tmp,
            )
            summary = render_summary(tl, W, H)
            # Independent expectation: pixel [128,0,127,255] on every pixel.
            expected = bytes([128, 0, 127, 255] * (W * H))
            digest = hashlib.sha256(expected).hexdigest()
            self.assertEqual(summary["frame_count"], 2)
            for entry in summary["frames"]:
                self.assertEqual(entry["pixel_sha256"], digest)
                self.assertEqual(entry["audio_alignment_error_samples"], 0)
            self.assertEqual(
                summary["frames"][0]["pixel_sha256"],
                summary["frames"][1]["pixel_sha256"],
            )

    def test_audio_alignment_error_off_boundary(self):
        # fps 2/1, 48kHz: frame 1 boundary at sample 24000; event at 24100.
        tl = make_timeline(events=[AudioEvent("e", 24100)], sample_rate=48000)
        self.assertEqual(audio_alignment_error(tl, 1), 100)
        self.assertIsNone(audio_alignment_error(make_timeline(), 0))

    def test_z_order_and_stable_tiebreak(self):
        # Same z: input order wins; later layer paints over earlier one.
        red = make_layer(name="r", png="r.png", z=0, order=0)
        green = make_layer(name="g", png="g.png", z=0, order=1)
        images = {
            "r": solid_image((255, 0, 0, 255)),
            "g": solid_image((0, 255, 0, 255)),
        }
        tl = make_timeline(layers=[red, green], frame_count=1)
        rgba = compose_frame(tl, images, W, H, 0)
        self.assertEqual(list(rgba[:4]), [0, 255, 0, 255])
        # Higher z paints on top regardless of input order.
        red2 = make_layer(name="r", png="r.png", z=5, order=0)
        tl2 = make_timeline(layers=[red2, green], frame_count=1)
        rgba2 = compose_frame(tl2, images, W, H, 0)
        self.assertEqual(list(rgba2[:4]), [255, 0, 0, 255])


if __name__ == "__main__":
    unittest.main()
