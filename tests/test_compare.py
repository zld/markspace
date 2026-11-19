"""Tests for cross-version comparison rules."""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from mediacheck.compare import (  # noqa: E402
    blocking_mismatches,
    compare_media,
    normalize_codec,
)
from mediacheck.model import (  # noqa: E402
    MediaFile,
    Mismatch,
    Track,
)


def media(*tracks: Track, duration: float | None = 1000.0,
          fatal: bool = False, name: str = "x") -> MediaFile:
    return MediaFile(path=name, size=1, sha256="0" * 64,
                     container="mp4", duration_ms=duration,
                     tracks=list(tracks), fatal=fatal)


def track(track_id: int = 1, kind: str = "video", codec: str = "avc1",
          time_base: str = "1/30000", duration: float | None = 1000.0,
          **detail) -> Track:
    return Track(track_id=track_id, kind=kind, codec=codec,
                 time_base=time_base, duration_ms=duration,
                 codec_detail=dict(detail))


def kinds(result) -> list[str]:
    return [m.kind for m in result.mismatches]


class CodecNormalizationTest(unittest.TestCase):
    def test_cross_container_equivalence(self) -> None:
        self.assertEqual(normalize_codec("avc1"), normalize_codec(
            "V_MPEG4/ISO/AVC"))
        self.assertEqual(normalize_codec("mp4a"), normalize_codec("A_AAC"))
        self.assertEqual(normalize_codec("Opus"), normalize_codec("A_OPUS"))

    def test_unknown_codec_falls_back_to_itself(self) -> None:
        self.assertEqual(normalize_codec("Banana"), "banana")


class IdentityTest(unittest.TestCase):
    def test_identical_media_matches(self) -> None:
        a = media(track(), track(2, "audio", "mp4a", "1/48000"))
        b = media(track(), track(2, "audio", "mp4a", "1/48000"))
        result = compare_media(a, b)
        self.assertEqual(result.status, "match")
        self.assertEqual(result.mismatches, [])
        self.assertEqual(blocking_mismatches(result), [])

    def test_equivalent_codecs_across_containers_do_not_mismatch(self) -> None:
        a = media(track(codec="avc1"))
        b = media(track(codec="V_MPEG4/ISO/AVC"))
        result = compare_media(a, b)
        self.assertEqual(result.mismatches, [])


class DurationRulesTest(unittest.TestCase):
    def test_within_tolerance_is_reported_but_not_blocking(self) -> None:
        a = media(track(), duration=1000.0)
        b = media(track(), duration=1012.0)
        result = compare_media(a, b, tolerance_ms=40)
        self.assertAlmostEqual(result.duration_delta_ms, 12.0)
        drift = [m for m in result.mismatches if m.kind == "duration_drift"]
        self.assertEqual(len(drift), 1)
        self.assertEqual(blocking_mismatches(result), [])

    def test_outside_tolerance_blocks(self) -> None:
        a = media(track(), duration=1000.0)
        b = media(track(), duration=1100.0)
        result = compare_media(a, b, tolerance_ms=40)
        blocking = blocking_mismatches(result)
        self.assertEqual([m.kind for m in blocking],
                         ["duration_out_of_tolerance"])
        self.assertEqual(result.status, "issues")

    def test_tolerance_is_configurable(self) -> None:
        a = media(track(duration=1000.0), duration=1000.0)
        b = media(track(duration=1012.0), duration=1012.0)
        self.assertEqual(compare_media(a, b, tolerance_ms=5).mismatches[0].kind,
                         "duration_out_of_tolerance")
        self.assertEqual(compare_media(a, b, tolerance_ms=10)
                         .mismatches[0].kind, "duration_out_of_tolerance")
        self.assertEqual(compare_media(a, b, tolerance_ms=50).mismatches[0].kind,
                         "duration_drift")

    def test_sub_epsilon_difference_is_silent(self) -> None:
        a = media(track(duration=1000.0), duration=1000.0)
        b = media(track(duration=1000.2), duration=1000.2)
        self.assertEqual(compare_media(a, b).mismatches, [])


class TrackPairingTest(unittest.TestCase):
    def test_codec_change_is_reported_with_offset(self) -> None:
        a = media(track(kind="audio", codec="mp4a", time_base="1/48000"))
        b = media(track(kind="audio", codec="A_OPUS", time_base="1/48000"))
        b.tracks[0].offset = 112
        result = compare_media(a, b)
        self.assertEqual(kinds(result), ["codec_change"])
        self.assertEqual(result.mismatches[0].offset, 112)
        self.assertEqual(result.mismatches[0].baseline_value, "aac")
        self.assertEqual(result.mismatches[0].candidate_value, "opus")

    def test_codec_parameter_change(self) -> None:
        a = media(track(width=320, height=180))
        b = media(track(width=640, height=180))
        result = compare_media(a, b)
        self.assertEqual(kinds(result), ["codec_parameter_change"])
        self.assertEqual(result.mismatches[0].field, "width")

    def test_time_base_change(self) -> None:
        a = media(track(time_base="1/30000"))
        b = media(track(time_base="1/90000"))
        result = compare_media(a, b)
        self.assertIn("time_base_change", kinds(result))
        self.assertEqual(blocking_mismatches(result), [])

    def test_missing_track(self) -> None:
        a = media(track(), track(2, "audio", "mp4a", "1/48000"))
        b = media(track())
        result = compare_media(a, b)
        self.assertEqual(kinds(result), ["track_missing"])
        self.assertEqual(result.mismatches[0].baseline_track_id, 2)

    def test_extra_track(self) -> None:
        a = media(track())
        b = media(track(), track(2, "audio", "mp4a", "1/48000"))
        result = compare_media(a, b)
        self.assertEqual(kinds(result), ["track_extra"])
        self.assertEqual(result.mismatches[0].candidate_track_id, 2)

    def test_type_mismatch_at_same_ordinal(self) -> None:
        a = media(track(1, "video"), track(2, "audio", "mp4a", "1/48000"))
        b = media(track(1, "audio", "mp4a", "1/48000"),
                  track(2, "video"))
        result = compare_media(a, b)
        self.assertIn("type_mismatch", kinds(result))

    def test_ambiguous_ordinal_pairing_lists_candidates(self) -> None:
        # Two AAC tracks in the candidate; the first ordinal is Opus but the
        # baseline's AAC exists later.  The comparator must not silently pair
        # it nor report a bare codec change.
        a = media(track(1, "audio", "mp4a", "1/48000"),
                  track(2, "audio", "mp4a", "1/48000"))
        b = media(track(1, "audio", "Opus", "1/48000"),
                  track(2, "audio", "mp4a", "1/48000"))
        result = compare_media(a, b)
        ambiguous = [m for m in result.mismatches
                     if m.kind == "ambiguous_pairing"]
        self.assertEqual(len(ambiguous), 1)
        self.assertEqual(ambiguous[0].candidates, [2])
        self.assertEqual(ambiguous[0].candidate_track_id, 1)

    def test_pairing_by_ordinal_across_kinds(self) -> None:
        a = media(track(1, "video"), track(2, "audio", "mp4a", "1/48000"))
        b = media(track(1, "audio", "mp4a", "1/48000"), track(2, "video"))
        # Kind matching must still pair video-with-video and audio-with-audio
        # rather than comparing a video against an audio track.
        result = compare_media(a, b)
        codec_changes = [m for m in result.mismatches
                         if m.kind == "codec_change"]
        self.assertEqual(codec_changes, [])

    def test_ignore_codec_suppresses_change(self) -> None:
        a = media(track(kind="audio", codec="mp4a", time_base="1/48000"))
        b = media(track(kind="audio", codec="A_OPUS", time_base="1/48000"))
        result = compare_media(a, b, ignore_fields=["codec"])
        self.assertNotIn("codec_change", kinds(result))

    def test_ignore_detail_field(self) -> None:
        a = media(track(width=320))
        b = media(track(width=640))
        result = compare_media(a, b, ignore_fields=["width"])
        self.assertEqual(result.mismatches, [])

    def test_ignore_duration(self) -> None:
        a = media(track(), duration=1000.0)
        b = media(track(duration=1200.0), duration=1200.0)
        result = compare_media(a, b, ignore_fields=["duration_ms"])
        self.assertEqual(result.mismatches, [])


class FatalInputTest(unittest.TestCase):
    def test_fatal_media_short_circuits(self) -> None:
        a = media(track(), fatal=True)
        b = media(track())
        result = compare_media(a, b)
        self.assertEqual(result.status, "probe_error")
        self.assertEqual(result.mismatches, [])


class SerializationTest(unittest.TestCase):
    def test_mismatch_round_trip(self) -> None:
        mismatch = Mismatch(kind="codec_change", message="m", field="codec")
        raw = mismatch.to_dict()
        self.assertEqual(raw["kind"], "codec_change")
        self.assertIsNone(raw["candidate_track_id"])

    def test_validation_dict_has_summary_keys(self) -> None:
        a = media(track(), duration=1000.0)
        b = media(track(kind="audio", codec="Opus", time_base="1/1000000"),
                  duration=1012.0)
        payload = compare_media(a, b, tolerance_ms=40).to_dict()
        for key in ("status", "baseline_tracks", "candidate_tracks",
                    "duration_delta_ms", "mismatches", "baseline",
                    "candidate"):
            self.assertIn(key, payload)
        self.assertEqual(payload["baseline_tracks"], 1)
        self.assertTrue(payload["mismatches"])
        for mismatch in payload["mismatches"]:
            self.assertIn("offset", mismatch)


if __name__ == "__main__":
    unittest.main()
