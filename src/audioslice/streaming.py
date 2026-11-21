"""Online, constant-memory counterpart of :func:`detector._build_segments`.

The offline segmenter keeps every window metric, whose size grows linearly
with recording length.  This segmenter consumes windows one at a time and
retains only run-level state (one open anchor plus a bounded pop chain), so
memory stays flat for arbitrarily long recordings.

It reproduces the offline interval semantics exactly:

* a voiced run shorter than ``min_segment_frames`` is a *pop*;
* each content-length run (anchor) expands left and right across pops while
  every crossed silence is shorter than ``min_silence_frames`` and no longer
  than ``max_pop_gap_frames``;
* a pop chain between two anchors merges them into one segment (interval
  union); two content runs directly adjacent over a short gap stay separate;
* a long silence finalises the open segment; pops left dangling beyond it are
  dropped until another anchor appears;
* an isolated pop with no anchor neighbour is dropped.
"""

from __future__ import annotations

from dataclasses import dataclass

from .detector import DetectorConfig, Segment, WindowMetric


@dataclass
class _Run:
    start_sample: int
    end_sample: int  # exclusive

    @property
    def length(self) -> int:
        return self.end_sample - self.start_sample


class StreamingSegmenter:
    """Build segments incrementally from labelled :class:`WindowMetric`s."""

    def __init__(self, cfg: DetectorConfig) -> None:
        self.cfg = cfg
        # Pops before any anchor; kept while their mutual gaps stay short.
        self._leading_pops: list[_Run] = []
        self._anchor: _Run | None = None
        self._pops: list[_Run] = []  # pops following the open anchor
        self._segments: list[Segment] = []
        self._total_frames = 0
        self._voiced = False
        self._current: _Run | None = None

    # ------------------------------------------------------------- window feed
    def add(self, metric: WindowMetric, is_voiced: bool) -> None:
        self._total_frames += metric.length
        if is_voiced and self._voiced:
            assert self._current is not None
            self._current.end_sample = metric.start_sample + metric.length
            return
        if is_voiced == self._voiced:
            return  # silent window inside an ongoing silent stretch
        if self._voiced and self._current is not None:
            self._on_run_closed(self._current)
        self._voiced = is_voiced
        self._current = (
            _Run(metric.start_sample, metric.start_sample + metric.length) if is_voiced else None
        )

    def _gap_ok(self, earlier: _Run, later: _Run) -> bool:
        gap = later.start_sample - earlier.end_sample
        return gap < self.cfg.min_silence_frames and gap <= self.cfg.max_pop_gap_frames

    def _emit(self, start: int, end: int) -> None:
        self._segments.append(Segment(start, end))

    def _seal_anchor(self) -> None:
        """Finalise the open anchor together with its reachable pops."""
        if self._anchor is None:
            self._pops.clear()
            return
        runs = [self._anchor, *self._pops]
        self._emit(min(r.start_sample for r in runs), max(r.end_sample for r in runs))
        self._anchor = None
        self._pops.clear()

    def _on_run_closed(self, run: _Run) -> None:
        is_pop = run.length < self.cfg.min_segment_frames
        chain = self._pops if self._anchor is not None else self._leading_pops
        last_voiced = chain[-1] if chain else self._anchor

        if self._anchor is None:
            if is_pop:
                if last_voiced is not None and not self._gap_ok(last_voiced, run):
                    self._leading_pops = [run]  # long gap: old chain unreachable
                else:
                    self._leading_pops.append(run)
                return
            # First anchor: walk the leading pop chain backwards, mirroring
            # the offline left expansion — stop at the first long gap.
            start = run.start_sample
            reachable_anchor = run
            for pop in reversed(self._leading_pops):
                if not self._gap_ok(pop, reachable_anchor):
                    break
                start = pop.start_sample
                reachable_anchor = pop
            self._anchor = _Run(start, run.end_sample)
            self._leading_pops.clear()
            return

        assert last_voiced is not None
        if not self._gap_ok(last_voiced, run):
            # Long silence: nothing later can reach the open anchor.
            self._seal_anchor()
            if is_pop:
                self._leading_pops = [run]
            else:
                self._anchor = run
            return

        if is_pop:
            self._pops.append(run)
            return

        # Content run reached across a short gap.
        if self._pops:
            # A pop chain separates two anchors: the offline interval union
            # merges anchor + pops + this anchor into a single segment whose
            # right end is the new anchor's end. Reopen as one open anchor so
            # further trailing pops can still extend it.
            self._anchor = _Run(self._anchor.start_sample, run.end_sample)
            self._pops.clear()
        else:
            # Directly adjacent content runs never merge; finalise the open
            # anchor (with any pops it right-absorbed earlier) and start anew.
            self._seal_anchor()
            self._anchor = run

    # ----------------------------------------------------------------- finalize
    def finalize(self) -> tuple[tuple[Segment, ...], tuple[Segment, ...]]:
        if self._current is not None and self._voiced:
            self._on_run_closed(self._current)
            self._current = None
        self._seal_anchor()
        if self._total_frames == 0:
            return (), ()
        if not self._segments:
            return (), (Segment(0, self._total_frames),)
        silences: list[Segment] = []
        cursor = 0
        for segment in self._segments:
            if segment.start_sample > cursor:
                silences.append(Segment(cursor, segment.start_sample))
            cursor = segment.end_sample
        if cursor < self._total_frames:
            silences.append(Segment(cursor, self._total_frames))
        return tuple(self._segments), tuple(silences)
