"""Matroska track assembly on top of the generic EBML iterator.

Track type, CodecID, DefaultDuration and per-track duration are read from
bytes.  A segment Duration gives media duration; otherwise block timecodes
from the clusters are used.
"""

from __future__ import annotations

from .binary import BinaryError, Reader
from .ebml import Element, iter_elements, issue, read_float, read_text, read_uint
from .mkvblocks import cluster_blocks
from .mkvdefs import (
    AUDIO,
    BIT_DEPTH,
    CHANNELS,
    CLUSTER,
    CODEC_ID,
    CODEC_PRIVATE,
    DEFAULT_DURATION,
    DEFAULT_TIMECODE_SCALE,
    DOCTYPE,
    DURATION,
    EBML_HEADER,
    INFO,
    LANGUAGE,
    PIXEL_HEIGHT,
    PIXEL_WIDTH,
    SAMPLING_FREQUENCY,
    SEGMENT,
    TIMECODE_SCALE,
    TITLE,
    TRACK_ENTRY,
    TRACK_NUMBER,
    TRACK_TYPE,
    TRACK_UID,
    TRACKS,
    VIDEO,
    VOID,
    kind_for_track_type,
)
from .model import (
    KIND_OTHER,
    Issue,
    MediaFile,
    SEVERITY_INFO,
    SEVERITY_WARNING,
    Track,
)

MAX_TRACKS = 256


def _decode_info(el: Element, media: MediaFile,
                 scale: int) -> tuple[int, float | None]:
    duration = None
    for child in iter_elements(el.body, media, 1):
        try:
            if child.id == TIMECODE_SCALE:
                scale = read_uint(child.body) or scale
            elif child.id == DURATION:
                duration = read_float(child.body)
            elif child.id == TITLE:
                read_text(child.body)
        except BinaryError as exc:
            media.add_issue(issue("mkv.info_truncated", exc.reason,
                                  exc.offset))
    return scale, duration


def _decode_video(el: Element, media: MediaFile, detail: dict) -> None:
    for child in iter_elements(el.body, media, 3):
        try:
            if child.id == PIXEL_WIDTH:
                detail["width"] = read_uint(child.body)
            elif child.id == PIXEL_HEIGHT:
                detail["height"] = read_uint(child.body)
        except BinaryError as exc:
            media.add_issue(issue("mkv.video_truncated", exc.reason,
                                  exc.offset))


def _decode_audio(el: Element, media: MediaFile, detail: dict) -> None:
    for child in iter_elements(el.body, media, 3):
        try:
            if child.id == SAMPLING_FREQUENCY:
                detail["sample_rate"] = read_float(child.body)
            elif child.id == CHANNELS:
                detail["channels"] = read_uint(child.body)
            elif child.id == BIT_DEPTH:
                detail["bit_depth"] = read_uint(child.body)
        except BinaryError as exc:
            media.add_issue(issue("mkv.audio_truncated", exc.reason,
                                  exc.offset))


def _decode_track_entry(el: Element, media: MediaFile, index: int) -> Track:
    number = index
    kind = KIND_OTHER
    codec = "unknown"
    language = None
    detail: dict = {}
    issues: list[Issue] = []

    for child in iter_elements(el.body, media, 2):
        try:
            if child.id == TRACK_NUMBER:
                number = read_uint(child.body)
            elif child.id == TRACK_UID:
                detail["track_uid"] = read_uint(child.body)
            elif child.id == TRACK_TYPE:
                kind = kind_for_track_type(read_uint(child.body))
            elif child.id == CODEC_ID:
                codec = read_text(child.body)
            elif child.id == CODEC_PRIVATE:
                detail["codec_private_size"] = child.size
            elif child.id == DEFAULT_DURATION:
                detail["default_duration_ms"] = read_uint(child.body) / 1e6
            elif child.id == LANGUAGE:
                language = read_text(child.body)
            elif child.id == VIDEO:
                _decode_video(child, media, detail)
            elif child.id == AUDIO:
                _decode_audio(child, media, detail)
        except BinaryError as exc:
            issues.append(issue("mkv.track_entry_truncated", exc.reason,
                                exc.offset))
    if codec == "unknown":
        issues.append(issue("mkv.missing_codec_id",
                            f"track {number} has no CodecID", el.start,
                            SEVERITY_WARNING))
    return Track(
        track_id=number,
        kind=kind,
        codec=codec,
        time_base=f"1/{DEFAULT_TIMECODE_SCALE}",
        duration_ms=None,
        language=language,
        offset=el.start,
        codec_detail=detail,
        issues=issues,
    )


def _decode_tracks(el: Element, media: MediaFile,
                   tracks: list[Track]) -> None:
    for entry in iter_elements(el.body, media, 2):
        if entry.id != TRACK_ENTRY:
            continue
        if len(tracks) >= MAX_TRACKS:
            media.add_issue(issue(
                "mkv.too_many_tracks",
                f"more than {MAX_TRACKS} tracks; extras ignored", entry.start))
            break
        tracks.append(_decode_track_entry(entry, media, len(tracks) + 1))


def _walk_segment(el: Element, media: MediaFile, scale: int,
                  segment_duration: float | None, tracks: list[Track],
                  track_ends: dict[int, float]) -> tuple[int, float | None]:
    for seg_el in iter_elements(el.body, media, 1):
        if seg_el.id == INFO:
            scale, segment_duration = _decode_info(seg_el, media, scale)
        elif seg_el.id == TRACKS:
            _decode_tracks(seg_el, media, tracks)
        elif seg_el.id == CLUSTER:
            for track_no, end_ms in cluster_blocks(seg_el, media, scale):
                if end_ms > track_ends.get(track_no, 0.0):
                    track_ends[track_no] = end_ms
        # Cues, SeekHead, Tags, Chapters, Attachments and anything else are
        # skipped by their checked length.  Their payloads are never
        # interpreted, so unknown data cannot drive further recursion.
    return scale, segment_duration


def _finalize(media: MediaFile, saw_header: bool, saw_segment: bool,
              doc_type: str | None, scale: int,
              segment_duration: float | None, tracks: list[Track],
              track_ends: dict[int, float]) -> None:
    if not saw_header:
        media.add_issue(issue("mkv.missing_ebml_header",
                              "file does not begin with an EBML header", 0))
    if not saw_segment:
        media.add_issue(issue("mkv.missing_segment",
                              "no Segment element found", 0))
    if doc_type not in (None, "matroska", "webm"):
        media.add_issue(issue(
            "mkv.unexpected_doctype",
            f"EBML DocType is {doc_type!r}, expected matroska or webm",
            0, SEVERITY_WARNING))

    duration_ms = None
    if segment_duration is not None and segment_duration > 0:
        duration_ms = segment_duration * scale / 1e6
    for track in tracks:
        end_ms = track_ends.get(track.track_id)
        if end_ms:
            track.duration_ms = end_ms
            track.codec_detail["duration_source"] = "cluster_blocks"
        elif duration_ms is not None:
            track.duration_ms = duration_ms
            track.codec_detail["duration_source"] = "segment_duration"
        track.time_base = f"1/{scale}"
        media.tracks.append(track)
    media.duration_ms = duration_ms
    if media.duration_ms is None:
        known = [t.duration_ms for t in media.tracks if t.duration_ms]
        media.duration_ms = max(known) if known else None
    media.container = "matroska"
    if doc_type:
        media.issues.append(Issue(code="mkv.doctype",
                                  message=f"DocType {doc_type!r}",
                                  offset=0, severity=SEVERITY_INFO))
    if any(i.code in ("mkv.missing_ebml_header", "mkv.missing_segment",
                      "mkv.element_overrun", "mkv.truncated_element_header",
                      "mkv.depth_exceeded") for i in media.issues):
        media.fatal = True


def probe_matroska(data: bytes, media: MediaFile) -> MediaFile:
    reader = Reader(data, 0, len(data), media.path)
    saw_header = False
    saw_segment = False
    doc_type = None
    scale = DEFAULT_TIMECODE_SCALE
    segment_duration = None
    tracks: list[Track] = []
    track_ends: dict[int, float] = {}

    for el in iter_elements(reader, media):
        if el.id == EBML_HEADER:
            saw_header = True
            for child in iter_elements(el.body, media, 1):
                try:
                    if child.id == DOCTYPE:
                        doc_type = read_text(child.body)
                except BinaryError as exc:
                    media.add_issue(issue("mkv.ebml_header_truncated",
                                          exc.reason, exc.offset))
        elif el.id == SEGMENT:
            saw_segment = True
            scale, segment_duration = _walk_segment(
                el, media, scale, segment_duration, tracks, track_ends)
        elif el.id == VOID:
            continue
        else:
            media.issues.append(Issue(
                code="mkv.unknown_top_level",
                message=f"skipping unknown top-level element 0x{el.id:x}",
                offset=el.start, severity=SEVERITY_INFO))

    _finalize(media, saw_header, saw_segment, doc_type, scale,
              segment_duration, tracks, track_ends)
    return media
