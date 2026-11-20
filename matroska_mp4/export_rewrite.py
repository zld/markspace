"""Rewrite path: normalize the global earliest PTS to zero.

DTS is regenerated monotonically from the sample durations in block (decode)
order, presentation times are preserved, and composition offsets become a
signed version-1 ``ctts``.  The track's start offset and inter-track offset are
retained with ``edts``/``elst`` so A/V sync is not shifted.
"""

from __future__ import annotations

from .matroska import ParsedMatroska
from .timeline import prepare
from .export_preserve import (ExportResult, _element_tree, _track_map,
                              _write_and_audit)


class RewriteError(Exception):
    """The input cannot be rewritten into a valid MP4 timeline."""


def run_rewrite(mkv: ParsedMatroska, timescale: int, output_path: str) -> ExportResult:
    timeline = prepare(mkv, timescale, "rewrite")
    result = ExportResult(path=output_path, mode="rewrite", timeline=timeline,
                          audit={}, element_tree=_element_tree(mkv),
                          unknown_size=mkv.document.unknown_sizes(),
                          track_map=_track_map(timeline))
    _write_and_audit(mkv, timeline, output_path, result)
    return result
