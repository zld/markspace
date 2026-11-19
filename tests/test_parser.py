"""Boundary tests for the box, sidx and stbl parsing layers."""
import struct
import unittest

from fragcheck.boxes import BoxError, find_child, parse_boxes
from fragcheck.sidx import SidxError, parse_sidx
from fragcheck.stbl import (StblError, parse_stco, parse_stsc, parse_stsz,
                            parse_stts, samples_per_chunk)


def box(t, payload):
    return struct.pack(">I4s", 8 + len(payload), t) + payload


def full(t, version, payload):
    return box(t, bytes((version,)) + b"\x00\x00\x00" + payload)


def only(data):
    boxes = parse_boxes(data)
    assert len(boxes) == 1
    return boxes[0]


class BoxParsing(unittest.TestCase):
    def test_simple_sequence(self):
        data = box(b"ftyp", b"isom") + box(b"free", b"x" * 6)
        boxes = parse_boxes(data)
        self.assertEqual([b.type for b in boxes], ["ftyp", "free"])
        self.assertEqual(boxes[1].offset, 12)
        self.assertEqual(boxes[1].end, len(data))

    def test_extended_size(self):
        payload = b"y" * 20
        data = struct.pack(">I4sQ", 1, b"mdat", 16 + len(payload)) + payload
        b = only(data)
        self.assertEqual((b.header_size, b.size), (16, 36))

    def test_zero_size_extends_to_parent_end(self):
        data = box(b"ftyp", b"isom") + struct.pack(">I4s", 0, b"mdat") + b"z" * 9
        boxes = parse_boxes(data)
        self.assertEqual(boxes[1].size, 8 + 9)
        self.assertEqual(boxes[1].end, len(data))

    def test_truncated_box_rejected(self):
        data = struct.pack(">I4s", 100, b"moov") + b"\x00" * 8
        with self.assertRaises(BoxError) as ctx:
            parse_boxes(data)
        self.assertEqual(ctx.exception.offset, 0)
        self.assertEqual(ctx.exception.box_type, "moov")

    def test_size_smaller_than_header_rejected(self):
        with self.assertRaises(BoxError):
            parse_boxes(struct.pack(">I4s", 4, b"free"))

    def test_trailing_garbage_rejected(self):
        with self.assertRaises(BoxError):
            parse_boxes(box(b"free", b"") + b"\x00\x01")

    def test_child_truncation_inside_parent_rejected(self):
        inner = struct.pack(">I4s", 500, b"trak") + b"\x00" * 4
        with self.assertRaises(BoxError):
            parse_boxes(box(b"moov", inner))

    def test_depth_limit(self):
        data = b""
        for _ in range(40):
            data = box(b"moov", data) if data else box(b"moov", b"")
        # nest 34 deep: innermost non-empty moov holds the chain
        data = b""
        for _ in range(34):
            data = box(b"moov", data)
        with self.assertRaises(BoxError):
            parse_boxes(data)

    def test_non_ascii_type_rejected(self):
        with self.assertRaises(BoxError):
            parse_boxes(struct.pack(">I4s", 8, b"\xff\xfe\x00\x01"))

    def test_container_children(self):
        data = box(b"moov", box(b"trak", box(b"free", b"\x00" * 4)))
        moov = only(data)
        self.assertEqual(find_child(moov, "trak").type, "trak")
        self.assertIsNone(find_child(moov, "mdia"))


def sidx_box(version, ept, first_offset, refs, timescale=1000, ref_id=1):
    body = struct.pack(">II", ref_id, timescale)
    if version == 0:
        body += struct.pack(">Ii", ept, first_offset)
    else:
        body += struct.pack(">Qq", ept, first_offset)
    body += struct.pack(">HH", 0, len(refs))
    for rtype, size, duration in refs:
        body += struct.pack(">III", (rtype << 31) | size, duration, 0x80000000)
    return full(b"sidx", version, body)


class SidxParsing(unittest.TestCase):
    def test_version0(self):
        data = sidx_box(0, 100, 8, [(0, 50, 10), (0, 60, 20)])
        s = parse_sidx(data, only(data))
        self.assertEqual(s.version, 0)
        self.assertEqual(s.earliest_presentation_time, 100)
        self.assertEqual(len(s.references), 2)
        start = s.end + 8
        self.assertEqual(s.reference_ranges(),
                         [(start, start + 50), (start + 50, start + 110)])

    def test_version1_64bit_times(self):
        data = sidx_box(1, 2**40, 0, [(0, 70, 30)])
        s = parse_sidx(data, only(data))
        self.assertEqual(s.earliest_presentation_time, 2**40)
        self.assertEqual(s.reference_ranges(), [(s.end, s.end + 70)])

    def test_negative_first_offset_exposed(self):
        data = sidx_box(1, 0, -16, [(0, 70, 30)])
        s = parse_sidx(data, only(data))
        self.assertEqual(s.first_offset, -16)
        self.assertEqual(s.reference_ranges()[0][0], s.end - 16)

    def test_hierarchical_reference_type(self):
        data = sidx_box(1, 0, 0, [(1, 40, 5), (0, 40, 5)])
        s = parse_sidx(data, only(data))
        self.assertEqual([r.reference_type for r in s.references], [1, 0])

    def test_truncated_rejected(self):
        with self.assertRaises(SidxError):
            parse_sidx(full(b"sidx", 1, b"\x00" * 3), only(full(b"sidx", 1, b"\x00" * 3)))

    def test_bad_version_rejected(self):
        data = full(b"sidx", 7, b"\x00" * 40)
        with self.assertRaises(SidxError):
            parse_sidx(data, only(data))

    def test_reference_count_mismatch_rejected(self):
        data = bytearray(sidx_box(1, 0, 0, [(0, 10, 1)]))
        # claim two references while the payload holds only one
        struct.pack_into(">H", data, 8 + 4 + 4 + 4 + 8 + 8 + 2, 2)
        with self.assertRaises(SidxError):
            parse_sidx(bytes(data), only(bytes(data)))


class StblParsing(unittest.TestCase):
    def test_stts(self):
        data = full(b"stts", 0, struct.pack(">III", 1, 5, 100)[0:4]
                    + struct.pack(">II", 5, 100))
        stts = parse_stts(data, only(data))
        self.assertEqual(stts.sample_count, 5)
        self.assertEqual(stts.total_delta, 500)

    def test_stsz_constant_and_explicit(self):
        const = full(b"stsz", 0, struct.pack(">II", 64, 7))
        stsz = parse_stsz(const, only(const))
        self.assertEqual(stsz.size_of(3), 64)
        explicit = full(b"stsz", 0, struct.pack(">IIII", 0, 2, 11, 22))
        stsz = parse_stsz(explicit, only(explicit))
        self.assertEqual(stsz.entries, [11, 22])

    def test_stsz_constant_with_stray_bytes_rejected(self):
        data = full(b"stsz", 0, struct.pack(">III", 64, 7, 1))
        with self.assertRaises(StblError):
            parse_stsz(data, only(data))

    def test_stco_and_co64(self):
        stco = full(b"stco", 0, struct.pack(">III", 2, 100, 200))
        got = parse_stco(stco, only(stco))
        self.assertEqual((got.offsets, got.wide), ([100, 200], False))
        co64 = full(b"co64", 0, struct.pack(">IQ", 1, 2**40))
        got = parse_stco(co64, only(co64))
        self.assertEqual((got.offsets, got.wide), ([2**40], True))

    def test_entry_count_cap(self):
        data = full(b"stts", 0, struct.pack(">I", 5_000_001))
        with self.assertRaises(StblError):
            parse_stts(data, only(data))

    def test_payload_length_mismatch_rejected(self):
        data = full(b"stsc", 0, struct.pack(">I", 2) + b"\x00" * 12)
        with self.assertRaises(StblError):
            parse_stsc(data, only(data))

    def test_samples_per_chunk_expansion(self):
        entries = [(1, 2, 1), (3, 5, 1)]
        self.assertEqual(samples_per_chunk(entries, 4), [2, 2, 5, 5])
        with self.assertRaises(StblError):
            samples_per_chunk([(2, 1, 1)], 3)  # must start at chunk 1
        with self.assertRaises(StblError):
            samples_per_chunk([(1, 1, 1), (1, 1, 1)], 3)  # not increasing


if __name__ == "__main__":
    unittest.main()
