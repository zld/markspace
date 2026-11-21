"""Tests for keyframe interpolation, blending, compositing and asset sandbox."""

from __future__ import annotations

import tempfile
import unittest
from fractions import Fraction
from pathlib import Path

import numpy as np

from shotline.assets import AssetError, load_asset, write_png
from shotline.compositor import (
    CompositorError,
    interpolate,
    preload_assets,
    render_frame,
)
from shotline.model import Keyframe, Layer, TimelineError, parse_timeline
from shotline.timebase import build_clock


def kf(t, x=0.0, y=0.0, scale=1.0, opacity=1.0, crop=(0.0, 0.0, 1.0, 1.0)):
    return Keyframe(float(t), float(x), float(y), float(scale), float(opacity), tuple(crop))


def solid(w, h, rgba):
    img = np.zeros((h, w, 4), dtype=np.uint8)
    img[:] = rgba
    return np.ascontiguousarray(img)


def timeline(layers, *, w=8, h=8, duration=1.0, fps=10):
    return parse_timeline(
        {"width": w, "height": h, "fps": fps, "duration": duration,
         "background": [0, 0, 0, 255], "layers": layers},
        base_dir=Path(tempfile.mkdtemp()),
    )


def layer_doc(lid, z, blend, frames, source="demo:gradient"):
    return {"id": lid, "source": source, "z": z, "blend": blend, "keyframes": frames}


class InterpolationTests(unittest.TestCase):
    def test_lerp_and_clamp(self):
        keys = (kf(0.0, x=0.0, opacity=1.0), kf(1.0, x=10.0, opacity=0.0))
        self.assertEqual(interpolate(keys, -1.0).x, 0.0)       # clamp before
        self.assertEqual(interpolate(keys, 2.0).x, 10.0)       # clamp after
        mid = interpolate(keys, 0.25)
        self.assertAlmostEqual(mid.x, 2.5)
        self.assertAlmostEqual(mid.opacity, 0.75)

    def test_crop_interpolation(self):
        keys = (kf(0.0, crop=(0.0, 0.0, 1.0, 1.0)),
                kf(2.0, crop=(0.2, 0.2, 0.8, 0.8)))
        state = interpolate(keys, 1.0)
        self.assertEqual(state.crop, (0.1, 0.1, 0.9, 0.9))


class CompositingTests(unittest.TestCase):
    def setUp(self):
        self.doc_frames = [{"t": 0.0, "x": 0, "y": 0, "scale": 1.0, "opacity": 1.0}]

    def test_z_order_and_stable_tie(self):
        def render(ids_zs):
            layers = [layer_doc(lid, z, "normal", self.doc_frames) for lid, z in ids_zs]
            tl = timeline(layers)
            clock = build_clock(tl)
            # Distinct opaque colours per layer so the top-most layer is visible.
            palette = {"a": (250, 10, 10, 255), "b": (10, 10, 250, 255)}
            assets = {l.id: solid(4, 4, palette[l.id]) for l in tl.layers}
            return render_frame(tl, assets, clock, 0)

        a = render([("a", 0), ("b", 1)])
        b = render([("b", 0), ("a", 1)])
        self.assertFalse(np.array_equal(a, b))                 # z changes winner
        self.assertEqual(tuple(a[0, 0]), (10, 10, 250, 255))   # b on top
        self.assertEqual(tuple(b[0, 0]), (250, 10, 10, 255))   # a on top
        tie_a = render([("a", 0), ("b", 0)])
        tie_b = render([("b", 0), ("a", 0)])
        self.assertFalse(np.array_equal(tie_a, tie_b))         # original order breaks ties
        self.assertEqual(tuple(tie_a[0, 0]), (10, 10, 250, 255))
        self.assertEqual(tuple(tie_b[0, 0]), (250, 10, 10, 255))

    def test_transparent_layer_is_noop(self):
        tl = timeline([layer_doc("a", 0, "normal", self.doc_frames)])
        clock = build_clock(tl)
        base_assets = {"a": solid(4, 4, (10, 20, 30, 255))}
        canvas = render_frame(tl, base_assets, clock, 0)
        tl2 = timeline([
            layer_doc("a", 0, "normal", self.doc_frames),
            layer_doc("ghost", 1, "normal", self.doc_frames),
        ])
        assets = {**base_assets, "ghost": solid(4, 4, (255, 0, 0, 0))}
        canvas2 = render_frame(tl2, assets, clock, 0)
        np.testing.assert_array_equal(canvas, canvas2)

    def test_opacity_scales_alpha(self):
        frames_hi = [{"t": 0.0, "x": 0, "y": 0, "scale": 1.0, "opacity": 1.0}]
        frames_lo = [{"t": 0.0, "x": 0, "y": 0, "scale": 1.0, "opacity": 0.25}]
        out = {}
        for name, frames in (("hi", frames_hi), ("lo", frames_lo)):
            tl = timeline([layer_doc("a", 0, "normal", frames)])
            out[name] = render_frame(tl, {"a": solid(8, 8, (255, 255, 255, 255))},
                                     build_clock(tl), 0)
        # Lower opacity on a white layer over a black bg must be darker.
        self.assertGreater(out["hi"][0, 0, 0], out["lo"][0, 0, 0])
        self.assertAlmostEqual(int(out["lo"][0, 0, 0]), round(255 * 0.25), delta=1)

    def test_blend_modes_hand_computed(self):
        bg = solid(8, 8, (200, 100, 50, 255))
        src = solid(8, 8, (80, 160, 240, 255))
        expected = {
            "multiply": (200 * 80 / 255, 100 * 160 / 255, 50 * 240 / 255),
            "screen": (255 - 55 * 175 / 255, 255 - 155 * 95 / 255,
                       255 - 205 * 15 / 255),
            "add": (255, 255, min(255, 50 + 240)),
        }
        for mode, rgb in expected.items():
            tl = timeline([layer_doc("a", 0, mode, self.doc_frames)])
            out = render_frame(tl, {"a": src}, build_clock(tl), 0)
            # Replace background on the same timeline for a controlled destination.
            tl = parse_timeline(
                {"width": 8, "height": 8, "fps": 10, "duration": 1.0,
                 "background": [200, 100, 50, 255],
                 "layers": [layer_doc("a", 0, mode, self.doc_frames)]},
                base_dir=tl.base_dir,
            )
            out = render_frame(tl, {"a": src}, build_clock(tl), 0)
            for c, want in enumerate(rgb):
                self.assertAlmostEqual(out[0, 0, c], round(want), delta=1,
                                       msg=f"{mode} channel {c}")

    def test_paint_on_empty_destination(self):
        tl = parse_timeline(
            {"width": 4, "height": 4, "fps": 10, "duration": 1.0,
             "background": [0, 0, 0, 0],
             "layers": [layer_doc("a", 0, "normal", self.doc_frames)]},
            base_dir=Path(tempfile.mkdtemp()),
        )
        out = render_frame(tl, {"a": solid(4, 4, (90, 110, 130, 255))},
                           build_clock(tl), 0)
        np.testing.assert_array_equal(out[0, 0], (90, 110, 130, 255))

    def test_negative_coordinates_clip(self):
        frames = [{"t": 0.0, "x": -6, "y": 0, "scale": 1.0, "opacity": 1.0}]
        tl = timeline([layer_doc("a", 0, "normal", frames)], w=8, h=8)
        # Left half black, right half white; only local cols 6..7 survive clipping.
        two_tone = solid(8, 8, (0, 0, 0, 255))
        two_tone[:, 4:] = (255, 255, 255, 255)
        out = render_frame(tl, {"a": two_tone}, build_clock(tl), 0)
        self.assertEqual(tuple(out[0, 0]), (255, 255, 255, 255))  # local col 6 -> white
        self.assertEqual(tuple(out[0, 1]), (255, 255, 255, 255))
        self.assertEqual(tuple(out[0, 2]), (0, 0, 0, 255))        # outside layer -> bg
        self.assertEqual(tuple(out[0, 7]), (0, 0, 0, 255))
        far = [{"t": 0.0, "x": 100, "y": 100, "scale": 1.0, "opacity": 1.0}]
        tl2 = timeline([layer_doc("a", 0, "normal", far)], w=8, h=8)
        out2 = render_frame(tl2, {"a": solid(8, 8, (255, 255, 255, 255))},
                            build_clock(tl2), 0)
        self.assertTrue(np.all(out2[..., :3] == 0))            # fully off-canvas

    def test_crop_and_scale_deterministic(self):
        frames = [{"t": 0.0, "x": 0, "y": 0, "scale": 2.0, "opacity": 1.0,
                   "crop": [0.25, 0.25, 0.75, 0.75]}]
        tl = timeline([layer_doc("a", 0, "normal", frames)], w=16, h=16)
        assets = preload_assets(tl, lambda l: solid(8, 8, (1, 2, 3, 255)))
        clock = build_clock(tl)
        np.testing.assert_array_equal(render_frame(tl, assets, clock, 0),
                                      render_frame(tl, assets, clock, 0))

    def test_missing_asset_fails(self):
        tl = timeline([layer_doc("a", 0, "normal", self.doc_frames)])
        with self.assertRaises(CompositorError):
            render_frame(tl, {}, build_clock(tl), 0)


class AssetSandboxTests(unittest.TestCase):
    def test_path_escape_and_missing_file(self):
        base = Path(tempfile.mkdtemp())
        tl = parse_timeline(
            {"width": 4, "height": 4, "fps": 10, "duration": 1.0,
             "layers": [layer_doc("a", 0, "normal",
                                  [{"t": 0.0}], source="../escape.png")]},
            base_dir=base,
        )
        with self.assertRaises(AssetError):
            load_asset("../escape.png", tl)
        with self.assertRaises(AssetError):
            load_asset("nope.png", tl)

    def test_local_png_roundtrip(self):
        base = Path(tempfile.mkdtemp())
        png = base / "card.png"
        write_png(png, solid(5, 6, (7, 8, 9, 255)))
        tl = parse_timeline(
            {"width": 4, "height": 4, "fps": 10, "duration": 1.0,
             "layers": [layer_doc("a", 0, "normal", [{"t": 0.0}], source="card.png")]},
            base_dir=base,
        )
        img = load_asset("card.png", tl)
        self.assertEqual(img.shape, (6, 5, 4))
        self.assertEqual(tuple(img[0, 0]), (7, 8, 9, 255))


class ValidationTests(unittest.TestCase):
    def base(self, **over):
        doc = {"width": 8, "height": 8, "fps": 10, "duration": 1.0,
               "layers": [{"id": "a", "source": "demo:badge", "z": 0,
                           "keyframes": [{"t": 0.0}, {"t": 0.5}]}]}
        doc.update(over)
        return doc

    def test_reject_bad_documents(self):
        with self.assertRaises(TimelineError):
            parse_timeline(self.base(width=5000))                    # canvas too large
        with self.assertRaises(TimelineError):
            parse_timeline(self.base(duration=0))                    # non-positive
        bad = self.base()
        bad["layers"][0]["keyframes"][1]["t"] = 0.0
        with self.assertRaises(TimelineError):
            parse_timeline(bad)                                      # duplicate t
        bad2 = self.base()
        bad2["layers"][0]["keyframes"][0]["opacity"] = 1.5
        with self.assertRaises(TimelineError):
            parse_timeline(bad2)                                     # opacity range
        with self.assertRaises(TimelineError):
            parse_timeline(self.base(fps=0))                         # bad fps
        bad3 = self.base()
        bad3["layers"][0]["blend"] = "dissolve"
        with self.assertRaises(TimelineError):
            parse_timeline(bad3)                                     # unknown blend
        with self.assertRaises(TimelineError):
            parse_timeline({"height": 8, "fps": 10, "duration": 1.0,
                            "layers": []})                           # missing width

    def test_reject_cue_outside_duration_and_duplicates(self):
        doc = self.base(cues=[{"sample_rate": 48000, "sample_index": 96000}])
        with self.assertRaises(TimelineError):
            parse_timeline(doc)
        doc = self.base(cues=[
            {"sample_rate": 48000, "sample_index": 100},
            {"sample_rate": 48000, "sample_index": 100},
        ])
        with self.assertRaises(TimelineError):
            parse_timeline(doc)


if __name__ == "__main__":
    unittest.main()
