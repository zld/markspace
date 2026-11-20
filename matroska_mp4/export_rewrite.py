"""Rewrite path: normalize the global earliest PTS to zero.

DTS is regenerated monotonically from the sample durations in block (decode)
order, presentation times are preserved, and composition offsets become a
signed version-1 ``ctts``.  The track's start offset and inter-track offset are
retained with ``edts``/``elst`` so A/V sync is not shifted.
"""

from __future__ import annotations

import os

from .matroska import ParsedMatroska
from .mp4_audit import audit_mp4
from .mp4_writer import temporary_output, write_mp4
from .timeline import prepare
from .export_preserve import ExportResult, _element_tree, _track_map


class RewriteError(Exception):
    """The input cannot be rewritten into a valid MP4 timeline."""


def run_rewrite(mkv: ParsedMatroska, timescale: int, output_path: str) -> ExportResult:
    timeline = prepare(mkv, timescale, "rewrite")
    result = ExportResult(path=output_path, mode="rewrite", timeline=timeline,
                          audit={}, element_tree=_element_tree(mkv),
                          unknown_size=mkv.document.unknown_sizes(),
                          track_map=_track_map(timeline))
    with temporary_output(output_path) as temp_path:
        write_mp4(mkv.path, mkv, timeline, temp_path)
        result.audit = audit_mp4(temp_path, mkv, timeline)
        if result.audit["sample_count"] != timeline.sample_count:
            raise audit_mp4.AuditError("audited sample count differs")
        os.replace(temp_path, output_path)
    return result
