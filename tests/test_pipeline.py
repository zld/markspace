"""End-to-end and unit tests for the sidxcheck pipeline (standard library only)."""

from __future__ import annotations

import io
import json
import struct
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

from sidxcheck import cli
from sidxcheck.boxes import parse_boxes
from sidxcheck.errors import (
    IndexValidationError,
    MediaStructureError,
    UnsupportedFeatureError,
)
from sidxcheck.fixtures import MovieSpec, build_movie, write_fixture
from sidxcheck.moovpatch import patch_moov
from sidxcheck.rewrite import rewrite_with_sidx
from sidxcheck.samplemap import build_sample_map
from sidxcheck.segments import plan_segments
from sidxcheck.sidx import parse_sidx_payload, serialize_sidx, Sidx, SidxReference
from sidxcheck.tables import load_track_tables, mdat_ranges
from sidxcheck.verify import check_file


def _load(data: bytes):
    fp = io.BytesIO(data)
    tree = parse_boxes(fp, len(data))
    tables = load_track_tables(fp, tree, 1)
    sample_map = build_sample_map(tables, mdat_ranges=mdat_ranges(tree))
    return fp, tree, tables, sample_map


class FixtureCase(unittest.TestCase):
    """Each test gets a scratch directory and helper builders."""

    def setUp(self) -> None:
        self._tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self._tmp.name)

    def tearDown(self) -> None:
        self._tmp.cleanup()

    def make(self, name: str, spec: MovieSpec | None = None, **kwargs) -> Path:
        return write_fixture(self.dir / name, spec, **kwargs)

    def rebuild(self, source: Path, name: str = "rebuilt.mp4", **kwargs):
        options = {"track_id": 1, "segment_ticks": 45_000}
        options.update(kwargs)
        return rewrite_with_sidx(source, self.dir / name, **options)


class BoxParsingTests(unittest.TestCase):
    def test_extended_64bit_size(self) -> None:
        payload = b"abcdef"
        data = struct.pack(">I4sQ", 1, b"mdat", 16 + len(payload)) + payload
        tree = parse_boxes(io.BytesIO(data), len(data))
        box = tree.top_level[0]
        self.assertEqual((box.header_size, box.payload_size), (16, 6))

    def test_zero_size_extends_to_end(self) -> None:
        data = struct.pack(">I4s", 0, b"mdat") + b"xyz"
        tree = parse_boxes(io.BytesIO(data), len(data))
        self.assertEqual(tree.top_level[0].end, len(data))

    def test_box_overrun_rejected(self) -> None:
        data = struct.pack(">I4s", 999, b"mdat") + b"xyz"
        with self.assertRaises(MediaStructureError):
            parse_boxes(io.BytesIO(data), len(data))

    def test_size_below_header_rejected(self) -> None:
        data = struct.pack(">I4s", 4, b"ftyp")
        with self.assertRaises(MediaStructureError):
            parse_boxes(io.BytesIO(data), len(data))

    def test_truncated_64bit_header_rejected(self) -> None:
        data = struct.pack(">I4s", 1, b"mdat") + b"1234"
        with self.assertRaises(MediaStructureError):
            parse_boxes(io.BytesIO(data), len(data))

    def test_trailing_garbage_rejected(self) -> None:
        data = struct.pack(">I4s", 8, b"free") + b"abc"
        with self.assertRaises(MediaStructureError):
            parse_boxes(io.BytesIO(data), len(data))

    def test_fragment_marker_rejected(self) -> None:
        data = struct.pack(">I4s", 8, b"moof")
        with self.assertRaises(UnsupportedFeatureError):
            parse_boxes(io.BytesIO(data), len(data))

    def test_child_overruns_container(self) -> None:
        child = struct.pack(">I4s", 40, b"free") + b"\x00" * 32
        container = struct.pack(">I4s", 8 + len(child) - 8, b"moov") + child
        with self.assertRaises(MediaStructureError):
            parse_boxes(io.BytesIO(container), len(container))

    def test_nesting_limit(self) -> None:
        inner = b""
        for _ in range(20):
            inner = struct.pack(">I4s", 8 + len(inner), b"moov") + inner
        with self.assertRaises(MediaStructureError):
            parse_boxes(io.BytesIO(inner), len(inner), max_depth=5)

    def test_box_count_limit(self) -> None:
        data = struct.pack(">I4s", 8, b"free") * 50
        with self.assertRaises(MediaStructureError):
            parse_boxes(io.BytesIO(data), len(data), max_boxes=10)


class SidxCodecTests(unittest.TestCase):
    def _ref(self, size: int = 100, duration: int = 10, sap: int = 1) -> SidxReference:
        return SidxReference(0, size, duration, sap, sap, 0)

    def test_roundtrip_v0(self) -> None:
        sidx = Sidx(0, 1, 90_000, 0, 40, [self._ref() for _ in range(3)])
        blob = serialize_sidx(sidx)
        parsed = parse_sidx_payload(blob[8:], box_start=0, box_end=len(blob))
        self.assertEqual(parsed.references, sidx.references)
        self.assertEqual(parsed.anchor_offset, len(blob) + 40)

    def test_roundtrip_v1_wide_values(self) -> None:
        refs = [self._ref()]
        sidx = Sidx(1, 7, 90_000, 2**40, 2**33, refs)
        blob = serialize_sidx(sidx)
        parsed = parse_sidx_payload(blob[8:], box_start=0, box_end=len(blob))
        self.assertEqual(parsed.first_offset, 2**33)
        self.assertEqual(parsed.earliest_presentation_time, 2**40)

    def test_negative_sap_type_rejected_on_write(self) -> None:
        sidx = Sidx(0, 1, 1000, 0, 0, [SidxReference(0, 10, 10, 0, 0, 0)])
        with self.assertRaises(IndexValidationError):
            serialize_sidx(sidx)

    def test_zero_reference_size_rejected_on_write(self) -> None:
        sidx = Sidx(0, 1, 1000, 0, 0, [SidxReference(0, 0, 10, 1, 1, 0)])
        with self.assertRaises(IndexValidationError):
            serialize_sidx(sidx)

    def test_trailing_bytes_rejected(self) -> None:
        sidx = Sidx(0, 1, 1000, 0, 0, [self._ref()])
        blob = serialize_sidx(sidx)
        with self.assertRaises(MediaStructureError):
            parse_sidx_payload(blob[8:] + b"\x00", box_start=0, box_end=len(blob) + 1)

    def test_reference_count_overrun_rejected(self) -> None:
        sidx = Sidx(0, 1, 1000, 0, 0, [self._ref()])
        blob = bytearray(serialize_sidx(sidx))
        struct.pack_into(">H", blob, len(blob) - 16, 5)
        with self.assertRaises(MediaStructureError):
            parse_sidx_payload(bytes(blob[8:]), box_start=0, box_end=len(blob))

    def test_reference_size_limit(self) -> None:
        sidx = Sidx(0, 1, 1000, 0, 0, [SidxReference(0, 1 << 31, 10, 1, 1, 0)])
        with self.assertRaises(IndexValidationError):
            serialize_sidx(sidx)


class SampleTableTests(FixtureCase):
    def test_stz2_co64_negative_ctts_map(self) -> None:
        spec = MovieSpec()
        _, _, tables, sample_map = _load(build_movie(spec))
        self.assertEqual(tables.stsz_kind, "stz2-16")
        self.assertEqual(sample_map.sample_count, 48)
        self.assertEqual(sample_map.media_duration, 180_000)
        self.assertLess(sample_map.composition_offset_min, 0)
        self.assertEqual(sample_map.samples[0].offset, 120)

    def test_stco_stsz_variant(self) -> None:
        spec = MovieSpec(use_co64=False, use_stz2=False)
        _, _, tables, sample_map = _load(build_movie(spec))
        self.assertEqual(tables.stsz_kind, "stsz")
        self.assertEqual(sample_map.sample_count, 48)

    def test_all_samples_sync_when_stss_absent(self) -> None:
        _, _, tables, sample_map = _load(build_movie(MovieSpec(include_stss=False)))
        self.assertTrue(tables.all_samples_sync)
        self.assertEqual(len(sample_map.sync_indices), 48)

    def test_stss_sample_number_past_end(self) -> None:
        data = bytearray(build_movie(MovieSpec()))
        tree = parse_boxes(io.BytesIO(data), len(data))
        stss = tree.first("stss", required=True)
        count = struct.unpack_from(">I", data, stss.start + 12)[0]
        # Replace the last, largest sample number with one past the track end.
        struct.pack_into(">I", data, stss.start + 16 + (count - 1) * 4, 999)
        with self.assertRaises(IndexValidationError):
            _load(bytes(data))

    def test_stts_sample_count_mismatch(self) -> None:
        with self.assertRaises(IndexValidationError):
            _load(build_movie(MovieSpec(stts_extra_samples=1)))
        with self.assertRaises(IndexValidationError):
            _load(build_movie(MovieSpec(stts_missing_samples=1)))

    def test_zero_length_sample_rejected(self) -> None:
        spec = MovieSpec(sample_sizes=[10] * 48)
        spec.sample_sizes[7] = 0
        with self.assertRaises(IndexValidationError):
            _load(build_movie(spec))

    def test_chunk_offsets_outside_mdat(self) -> None:
        with self.assertRaises(IndexValidationError):
            _load(build_movie(MovieSpec(chunk_offset_bias=100_000)))

    def test_truncated_file_rejected(self) -> None:
        data = build_movie(MovieSpec())
        with self.assertRaises(MediaStructureError):
            parse_boxes(io.BytesIO(data[:100]), 100)

    def test_moof_marker_rejected(self) -> None:
        with self.assertRaises(UnsupportedFeatureError):
            parse_boxes(io.BytesIO(build_movie(MovieSpec(append_moof=True))),
                        len(build_movie(MovieSpec(append_moof=True))))

    def test_encrypted_sample_entry_rejected(self) -> None:
        with self.assertRaises(UnsupportedFeatureError):
            _load(build_movie(MovieSpec(encrypted_sample_entry=True)))

    def test_missing_table_rejected(self) -> None:
        data = bytearray(build_movie(MovieSpec()))
        tree = parse_boxes(io.BytesIO(data), len(data))
        stsc = tree.first("stsc", required=True)
        data[stsc.start + 4 : stsc.start + 8] = b"junk"
        with self.assertRaises(MediaStructureError):
            tree2 = parse_boxes(io.BytesIO(bytes(data)), len(data))
            fp = io.BytesIO(bytes(data))
            load_track_tables(fp, tree2, 1)


class SegmentTests(FixtureCase):
    def test_four_even_segments(self) -> None:
        _, _, _, sample_map = _load(build_movie(MovieSpec()))
        plan = plan_segments(sample_map, target_ticks=45_000)
        self.assertEqual(len(plan.segments), 4)
        for segment in plan.segments:
            self.assertEqual(segment.sample_count, 12)
            self.assertEqual(segment.duration_ticks, 45_000)
            self.assertEqual(segment.byte_size, segment.byte_end - segment.byte_start)

    def test_drift_allowed(self) -> None:
        _, _, _, sample_map = _load(build_movie(MovieSpec()))
        plan = plan_segments(sample_map, target_ticks=50_000, max_drift_ticks=10_000)
        self.assertEqual(len(plan.segments), 4)
        self.assertEqual(plan.segments[0].duration_ticks, 45_000)

    def test_drift_exceeded_reports_failure(self) -> None:
        _, _, _, sample_map = _load(build_movie(MovieSpec()))
        with self.assertRaises(IndexValidationError) as ctx:
            plan_segments(sample_map, target_ticks=50_000, max_drift_ticks=0)
        self.assertIn("nearest sync sample", str(ctx.exception))

    def test_single_segment_when_target_covers_track(self) -> None:
        _, _, _, sample_map = _load(build_movie(MovieSpec()))
        plan = plan_segments(sample_map, target_ticks=10_000_000)
        self.assertEqual(len(plan.segments), 1)
        self.assertEqual(plan.segments[0].sample_count, 48)


class RewriteTests(FixtureCase):
    def test_wrong_offsets_repaired(self) -> None:
        source = self.make("original.mp4")
        before = check_file(source, track_id=1)
        self.assertFalse(before.primary_index.offset_ok)
        self.assertTrue(before.primary_index.duration_ok)
        result = self.rebuild(source)
        after = check_file(self.dir / "rebuilt.mp4", track_id=1)
        self.assertTrue(after.ok)
        self.assertEqual(after.primary_index.offset_delta, 0)
        self.assertEqual(result.segments, 4)
        self.assertEqual(after.sample_entries, 48)

    def test_source_is_untouched(self) -> None:
        source = self.make("original.mp4")
        original = source.read_bytes()
        self.rebuild(source)
        self.assertEqual(source.read_bytes(), original)

    def test_insert_when_no_index(self) -> None:
        source = self.make("original.mp4", MovieSpec(old_index="none"))
        with self.assertRaises(IndexValidationError):
            check_file(source, track_id=1)
        result = self.rebuild(source)
        after = check_file(self.dir / "rebuilt.mp4", track_id=1)
        self.assertTrue(after.ok)
        self.assertGreater(result.offset_shift, 0)

    def test_growth_shifts_mdat(self) -> None:
        source = self.make("original.mp4", MovieSpec(sidx_omit_tail=3))
        result = self.rebuild(source)
        self.assertGreater(result.offset_shift, 0)
        self.assertTrue(check_file(self.dir / "rebuilt.mp4", track_id=1).ok)

    def test_shrink_shifts_mdat_back(self) -> None:
        # A v1 legacy index is 8 bytes larger than the v0 index we write, so the
        # rebuilt file is shorter and mdat moves earlier.
        source = self.make("original.mp4", MovieSpec(sidx_version=1))
        result = self.rebuild(source)
        self.assertLess(result.offset_shift, 0)
        self.assertTrue(check_file(self.dir / "rebuilt.mp4", track_id=1).ok)

    def test_all_table_variants_roundtrip(self) -> None:
        variants = {
            "stco-stsz": MovieSpec(use_co64=False, use_stz2=False),
            "stz2-8": MovieSpec(stz2_field_size=8),
            "stz2-4": MovieSpec(stz2_field_size=4, use_co64=False),
            "no-stss": MovieSpec(include_stss=False),
            "no-ctts": MovieSpec(include_ctts=False),
        }
        for label, spec in variants.items():
            with self.subTest(label=label):
                source = self.make(f"{label}.mp4", spec)
                self.rebuild(source, f"{label}-rebuilt.mp4")
                self.assertTrue(
                    check_file(self.dir / f"{label}-rebuilt.mp4", track_id=1).ok
                )

    def test_nested_reference_rejected(self) -> None:
        source = self.make("original.mp4", MovieSpec(sidx_nested=True))
        result = check_file(source, track_id=1)
        self.assertFalse(result.ok)
        messages = " ".join(result.primary_index.problems)
        self.assertIn("hierarchical", messages)

    def test_stale_trailing_index_dropped(self) -> None:
        from sidxcheck.boxes import parse_boxes
        from sidxcheck.mp4write import sidx_box, SegmentRefSpec

        source = self.make("original.mp4")
        stale = sidx_box(
            version=0,
            reference_id=1,
            timescale=90_000,
            earliest_presentation_time=0,
            first_offset=0,
            references=[SegmentRefSpec(size=100, duration=45_000)],
        )
        source.write_bytes(source.read_bytes() + stale)
        rebuilt = self.dir / "rebuilt.mp4"
        rewrite_with_sidx(source, rebuilt, track_id=1, segment_ticks=45_000)
        tree = parse_boxes(io.BytesIO(rebuilt.read_bytes()), rebuilt.stat().st_size)
        self.assertEqual(len(tree.all("sidx")), 1)
        self.assertTrue(check_file(rebuilt, track_id=1).ok)

    def test_same_path_rejected(self) -> None:
        source = self.make("original.mp4")
        from sidxcheck.errors import IOErrorFailure

        with self.assertRaises(IOErrorFailure):
            rewrite_with_sidx(
                source, source, track_id=1, segment_ticks=45_000
            )

    def test_unknown_track_rejected(self) -> None:
        source = self.make("original.mp4")
        with self.assertRaises(MediaStructureError):
            rewrite_with_sidx(source, self.dir / "x.mp4", track_id=9, segment_ticks=1)


class LayoutConvergenceTests(FixtureCase):
    def test_layout_replans_until_moov_stable(self) -> None:
        from unittest import mock

        import sidxcheck.rewrite as rw
        from sidxcheck.boxes import parse_boxes
        from sidxcheck.samplemap import build_sample_map
        from sidxcheck.segments import plan_segments
        from sidxcheck.tables import load_track_tables, mdat_ranges

        data = build_movie(MovieSpec(use_co64=False, use_stz2=False))
        tree = parse_boxes(io.BytesIO(data), len(data))
        fp = io.BytesIO(data)
        sample_map = build_sample_map(
            load_track_tables(fp, tree, 1), mdat_ranges=mdat_ranges(tree)
        )
        plan = plan_segments(sample_map, target_ticks=45_000)
        moov = tree.first("moov", required=True)
        payload = data[moov.start + 8 : moov.end]
        real = rw.patch_moov
        state = {"calls": 0}

        def first_call_grows(payload_bytes, shift, *, where="moov"):
            state["calls"] += 1
            out = real(payload_bytes, shift, where=where)
            if state["calls"] == 1:
                return rw.MoovPatch(
                    data=out.data + b"\x00" * 48,
                    upgrades=out.upgrades,
                    patched_entries=out.patched_entries,
                    warnings=out.warnings,
                )
            return out

        with mock.patch.object(rw, "patch_moov", first_call_grows):
            layout = rw._layout(
                tree,
                sample_map,
                plan,
                existing=None,
                moov=moov,
                moov_payload=payload,
                first_mdat=tree.all("mdat")[0],
            )
        self.assertGreaterEqual(layout.passes, 2)
        self.assertTrue(any("stabilized after" in text for text in layout.warnings))


class MoovPatchTests(unittest.TestCase):
    def test_offset_upgrade_grows_and_converts(self) -> None:
        from sidxcheck.fixtures import _moov

        spec = MovieSpec(use_co64=False, use_stz2=False)
        moov_payload = _moov(spec, [100 + 100 * i for i in range(12)])[8:]
        upgraded = patch_moov(moov_payload, lambda value: 0xFFFFFFFF - 200)
        self.assertEqual(upgraded.upgrades, 1)
        self.assertEqual(upgraded.patched_entries, 12)
        self.assertEqual(len(upgraded.data), len(moov_payload) + 48)

    def test_idempotent_shift(self) -> None:
        from sidxcheck.fixtures import _moov

        spec = MovieSpec(use_co64=False, use_stz2=False)
        moov_payload = _moov(spec, [100 + 100 * i for i in range(12)])[8:]
        shifted = patch_moov(moov_payload, lambda value: 40)
        self.assertEqual(len(shifted.data), len(moov_payload))
        self.assertEqual(shifted.upgrades, 0)


class CliTests(FixtureCase):
    def _run(self, argv: list[str]) -> tuple[int, str, str]:
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = cli.main(argv)
        return code, out.getvalue(), err.getvalue()

    def test_demo_json_contract(self) -> None:
        code, out, _ = self._run(
            [
                "demo",
                "--dir",
                str(self.dir / "demo"),
                "--samples",
                "48",
                "--timescale",
                "90000",
                "--sample-duration",
                "3750",
                "--segment-ticks",
                "45000",
                "--json",
            ]
        )
        self.assertEqual(code, 0)
        payload = json.loads(out)
        self.assertEqual(payload["sample_entries"], 48)
        self.assertFalse(payload["old_index_offset_ok"])
        self.assertTrue(payload["rebuilt_index_offset_ok"])
        self.assertEqual(payload["index_offset_delta"], 0)
        self.assertEqual(payload["segment_count"], 4)
        self.assertEqual(len(payload["boundaries"]), 4)
        for boundary in payload["boundaries"]:
            self.assertEqual(boundary["sample_count"], 12)
            self.assertEqual(boundary["duration_ticks"], 45000)
            self.assertLess(boundary["byte_start"], boundary["byte_end"])

    def test_check_bad_index_exit_2(self) -> None:
        source = self.make("original.mp4")
        code, _, _ = self._run(["check", str(source), "--track-id", "1", "--json"])
        self.assertEqual(code, 2)

    def test_check_missing_file_exit_3(self) -> None:
        code, _, err = self._run(["check", str(self.dir / "nope.mp4"), "--json"])
        self.assertEqual(code, 3)
        self.assertIn("cannot", err.lower() + "cannot")

    def test_rebuild_cli(self) -> None:
        source = self.make("original.mp4")
        output = self.dir / "cli-rebuilt.mp4"
        code, out, _ = self._run(
            [
                "rebuild",
                str(source),
                "--output",
                str(output),
                "--track-id",
                "1",
                "--segment-ticks",
                "45000",
                "--json",
            ]
        )
        self.assertEqual(code, 0)
        payload = json.loads(out)
        self.assertTrue(payload["rebuilt_index_offset_ok"])
        self.assertTrue(output.exists())

    def test_bad_structure_exit_2(self) -> None:
        broken = self.dir / "broken.mp4"
        broken.write_bytes(build_movie(MovieSpec(truncate_at=60)))
        code, _, _ = self._run(["check", str(broken), "--json"])
        self.assertEqual(code, 2)


if __name__ == "__main__":
    unittest.main()
