"""MP4/MOV (ISO BMFF) track assembly on top of :mod:`mediacheck.bmff`.

A broken child stops only the current track; the diagnostic is attached to
that track and the remaining tracks continue to be read.
"""

from __future__ import annotations

from .binary import BinaryError, Reader, ticks_to_ms
from .bmff import (
    CONTAINER_BOXES,
    ELST,
    FTYP,
    HANDLER_KINDS,
    HDLR,
    MDHD,
    MOOV,
    MVHD,
    STBL,
    STSD,
    TKHD,
    TRAK,
    Box,
    issue,
    iter_boxes,
    parse_elst,
    parse_hdlr,
    parse_mdhd,
    parse_mvhd,
    parse_stsd,
    parse_tkhd,
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


class _TrackBuilder:
    """Accumulates the fields of one trak while its boxes are walked."""

    def __init__(self, fallback_id: int):
        self.track_id = fallback_id
        self.offset: int | None = None
        self.timescale = 0
        self.media_duration = 0
        self.tkhd_duration = 0
        self.handler = ""
        self.stsd_box: Box | None = None
        self.edit_duration: int | None = None
        self.issues: list[Issue] = []

    def walk(self, reader: Reader, media: MediaFile, depth: int) -> None:
        for child in iter_boxes(reader, media, depth, "trak"):
            if child.type == TKHD:
                self._on_tkhd(child)
            elif child.type == MDHD:
                self._on_mdhd(child)
            elif child.type == HDLR:
                try:
                    self.handler = parse_hdlr(child)
                except BinaryError as exc:
                    self.issues.append(issue("mp4.hdlr_truncated", exc.reason,
                                             exc.offset, track_id=self.track_id))
            elif child.type == ELST:
                self._on_elst(child)
            elif child.type == STSD:
                self.stsd_box = child
            else:
                self._descend(child, media, depth)

    def _descend(self, child: Box, media: MediaFile, depth: int) -> None:
        """Walk known containers so nested metadata is still reached."""
        if child.type in CONTAINER_BOXES:
            if child.type == STBL:
                self._walk_stbl(child, media, depth + 1)
            else:
                self.walk(child.body, media, depth + 1)

    def _walk_stbl(self, box: Box, media: MediaFile, depth: int) -> None:
        for child in iter_boxes(box.body, media, depth, "stbl"):
            if child.type == STSD:
                self.stsd_box = child

    def _on_tkhd(self, box: Box) -> None:
        try:
            self.track_id, self.tkhd_duration = parse_tkhd(box)
        except BinaryError as exc:
            self.issues.append(issue("mp4.tkhd_truncated", exc.reason,
                                     exc.offset, track_id=self.track_id))

    def _on_mdhd(self, box: Box) -> None:
        try:
            self.timescale, self.media_duration, unknown = parse_mdhd(box)
        except BinaryError as exc:
            self.issues.append(issue("mp4.mdhd_truncated", exc.reason,
                                     exc.offset, track_id=self.track_id))
            return
        if unknown:
            self.issues.append(issue(
                "mp4.mdhd_unknown_duration",
                "mdhd duration uses the all-ones unknown marker",
                box.start, SEVERITY_WARNING, self.track_id))

    def _on_elst(self, box: Box) -> None:
        try:
            entries = parse_elst(box)
        except BinaryError as exc:
            self.issues.append(issue("mp4.elst_truncated", exc.reason,
                                     exc.offset, track_id=self.track_id))
            return
        if entries:
            self.edit_duration = entries[0][0]

    def build(self) -> Track | None:
        kind = HANDLER_KINDS.get(self.handler, KIND_OTHER)
        if self.handler and kind == KIND_OTHER:
            self.issues.append(issue(
                "mp4.unknown_handler",
                f"handler {self.handler!r} is not audio/video/subtitle",
                0, SEVERITY_INFO, self.track_id))
        codec, detail = "unknown", {}
        if self.stsd_box is not None:
            try:
                codec, detail = parse_stsd(self.stsd_box, kind, self.issues,
                                           self.track_id)
            except BinaryError as exc:
                self.issues.append(issue("mp4.stsd_truncated", exc.reason,
                                         exc.offset, track_id=self.track_id))
        elif self.timescale:
            self.issues.append(issue(
                "mp4.missing_stsd",
                f"track {self.track_id} has mdhd but no stsd",
                0, SEVERITY_WARNING, self.track_id))

        if not self.timescale:
            self.issues.append(issue(
                "mp4.missing_mdhd",
                f"track {self.track_id} has no usable mdhd timescale",
                0, track_id=self.track_id))
            return Track(track_id=self.track_id, kind=kind, codec=codec,
                         time_base="0/0", duration_ms=None,
                         offset=self.offset,
                         codec_detail=detail, issues=self.issues)

        duration = self.media_duration or self.edit_duration or 0
        return Track(
            track_id=self.track_id,
            kind=kind,
            codec=codec,
            time_base=f"1/{self.timescale}",
            duration_ms=ticks_to_ms(duration, self.timescale),
            offset=self.offset,
            codec_detail=detail,
            issues=self.issues,
        )


def probe_mp4(data: bytes, media: MediaFile) -> MediaFile:
    reader = Reader(data, 0, len(data), media.path)
    saw_ftyp = False
    moov_box: Box | None = None

    for box in iter_boxes(reader, media):
        if box.type == FTYP:
            saw_ftyp = True
            head = box.body
            if head.remaining >= 4:
                brand = head.read_bytes(4).decode("latin-1", "replace")
                media.issues.append(Issue(
                    code="mp4.brand",
                    message=f"major brand {brand!r}",
                    offset=box.start,
                    severity=SEVERITY_INFO))
        elif box.type == MOOV:
            if moov_box is not None:
                media.add_issue(issue("mp4.duplicate_moov",
                                      "more than one moov box; using the last",
                                      box.start, SEVERITY_WARNING))
            moov_box = box

    media.container = "mp4"
    if not saw_ftyp:
        media.add_issue(issue("mp4.missing_ftyp",
                              "file does not begin with an ftyp box", 0))
    if moov_box is None:
        media.add_issue(issue("mp4.missing_moov",
                              "no moov box found; cannot read track metadata",
                              0))
        media.fatal = True
        return media

    movie_timescale, movie_duration = _read_movie_header(moov_box, media)
    _read_tracks(moov_box, media)
    if movie_timescale > 0 and movie_duration:
        media.duration_ms = ticks_to_ms(movie_duration, movie_timescale)
    elif media.tracks:
        durations = [t.duration_ms for t in media.tracks
                     if t.duration_ms is not None]
        media.duration_ms = max(durations) if durations else None

    if any(i.code == "mp4.box_overrun" for i in media.issues):
        media.fatal = True
    return media


def _read_movie_header(moov: Box, media: MediaFile) -> tuple[int, int]:
    for child in iter_boxes(moov.body, media, 1, "moov"):
        if child.type == MVHD:
            try:
                return parse_mvhd(child)
            except BinaryError as exc:
                media.add_issue(issue("mp4.mvhd_truncated", exc.reason,
                                      exc.offset))
                return 0, 0
    return 0, 0


def _read_tracks(moov: Box, media: MediaFile) -> None:
    builders: list[tuple[_TrackBuilder, Box]] = []
    for child in iter_boxes(moov.body, media, 1, "moov"):
        if child.type != TRAK:
            continue
        if len(builders) >= MAX_TRACKS:
            media.add_issue(issue(
                "mp4.too_many_tracks",
                f"more than {MAX_TRACKS} trak boxes; extras ignored",
                child.start))
            break
        builders.append((_TrackBuilder(len(builders) + 1), child))

    for builder, trak in builders:
        builder.offset = trak.start
        builder.walk(trak.body, media, 2)
        track = builder.build()
        if track is not None:
            media.tracks.append(track)
