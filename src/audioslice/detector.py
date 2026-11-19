"""Segment / silence detection — the single source of truth for boundaries.

Both the CLI and the export pipeline call :func:`detect`; boundary logic is
never duplicated elsewhere.  Pipeline: frame RMS -> noise floor from the
quietest frames -> margin/hysteresis thresholds -> hysteresis frame labelling
-> sample-accurate edge refinement inside transition frames -> minimum-duration
resolution (short gaps bridged, isolated blips absorbed, boundary silence kept).
"""

from __future__ import annotations

import numpy as np

from .model import AnalysisParams, AudioData, DetectionResult, Region

#: Safety floor so digital silence cannot yield zero thresholds every frame
#: would cross.  -240 dBFS is below any representable real-world PCM content.
_EPSILON = 1e-12


def frame_rms(samples: np.ndarray, frame_samples: int) -> tuple[np.ndarray, np.ndarray]:
    """Return per-frame RMS values and their half-open ``[start, end)`` edges."""
    total = samples.shape[0]
    full_frames, remainder = divmod(total, frame_samples)
    frame_count = full_frames + (1 if remainder else 0)
    edges = np.empty(frame_count + 1, dtype=np.int64)
    edges[:-1] = np.arange(frame_count) * frame_samples
    edges[-1] = total
    rms = np.empty(frame_count, dtype=np.float64)
    for i in range(frame_count):
        block = samples[edges[i] : edges[i + 1]]
        rms[i] = np.sqrt(np.mean(np.square(block, dtype=np.float64)))
    return rms, edges


def _refine_boundary(
    samples: np.ndarray,
    frame_start: int,
    frame_end: int,
    threshold: float,
    entering: bool,
) -> int:
    """Locate a threshold crossing at sample resolution inside one frame.

    Uses a short sliding window (1/8 of the frame) and prefix sums of squared
    samples so every window RMS is evaluated in constant time.
    """
    span = frame_end - frame_start
    window = max(1, span // 8)
    if span <= window:
        return frame_start if entering else frame_end

    squared = np.square(samples[frame_start:frame_end], dtype=np.float64)
    prefix = np.concatenate(([0.0], np.cumsum(squared)))
    # Window RMS for every start position p: [p, p + window) within the frame.
    starts = np.arange(span - window + 1)
    means = (prefix[starts + window] - prefix[starts]) / window
    window_rms = np.sqrt(means)
    crossing = np.flatnonzero(window_rms >= threshold)
    if crossing.size == 0:
        return frame_start if entering else frame_end
    if entering:
        return frame_start + int(crossing[0])
    return frame_start + int(crossing[-1]) + window


def _classify_frames(rms: np.ndarray, enter_threshold: float, exit_threshold: float) -> np.ndarray:
    """Hysteresis labelling: True = active, False = silence."""
    labels = np.zeros(rms.shape[0], dtype=bool)
    active = False
    for i, level in enumerate(rms):
        if not active:
            if level >= enter_threshold:
                active = True
        elif level <= exit_threshold:
            active = False
        labels[i] = active
    return labels


def _runs_from_labels(
    labels: np.ndarray,
    edges: np.ndarray,
    samples: np.ndarray,
    enter_threshold: float,
    exit_threshold: float,
) -> list[tuple[str, int, int]]:
    """Turn frame labels into a contiguous, alternating sample-space tiling.

    Each maximal run of active frames becomes a segment whose enter/exit edges
    are refined to the exact threshold-crossing sample inside the transition
    frame.  Every gap before, between or after the refined segment spans is
    emitted as silence, so the returned runs share boundaries and cover
    ``[0, total)`` with no orphan samples or zero-length runs.
    """
    total = int(edges[-1])
    n = labels.shape[0]

    # Collect the refined span of every maximal run of active frames.
    segments: list[tuple[int, int]] = []
    i = 0
    while i < n:
        if not labels[i]:
            i += 1
            continue
        j = i
        while j + 1 < n and labels[j + 1]:
            j += 1
        seg_start = _refine_boundary(
            samples, int(edges[i]), int(edges[i + 1]), enter_threshold, True
        )
        if j + 1 < n:
            seg_end = _refine_boundary(
                samples, int(edges[j + 1]), int(edges[j + 2]), exit_threshold, False
            )
        else:
            seg_end = total
        segments.append((int(seg_start), int(seg_end)))
        i = j + 1

    # Fill every gap around the refined spans with silence, enforcing a strict
    # left-to-right tiling even if a refined edge crosses a neighbor.
    tiled: list[tuple[str, int, int]] = []
    cursor = 0
    for seg_start, seg_end in segments:
        seg_start = max(seg_start, cursor)
        seg_end = max(seg_end, seg_start)
        if seg_start > cursor:
            tiled.append(("silence", cursor, seg_start))
        tiled.append(("segment", seg_start, seg_end))
        cursor = seg_end
    if cursor < total:
        tiled.append(("silence", cursor, total))
    return tiled


def _collapse(runs: list[list]) -> list[list]:
    """Merge adjacent runs of the same kind while keeping the spans contiguous.

    Runs arrive as a tiling partition.  When several same-kind runs become
    adjacent after a reclassification they must cover the union of their spans,
    so each emitted run starts at the running cursor (the previous run's end)
    rather than at the first member's stored start, which can lag behind.
    """
    collapsed: list[list] = []
    cursor = 0
    for kind, start, end in runs:
        start = max(int(start), cursor)
        end = max(int(end), start)
        if collapsed and collapsed[-1][0] == kind:
            collapsed[-1][2] = end  # extend across the absorbed neighbor
        elif end > start:
            collapsed.append([kind, start, end])
        cursor = end
    return collapsed


def _resolve_minimum_durations(
    runs: list[tuple[str, int, int]],
    min_silence_samples: int,
    min_segment_samples: int,
) -> list[tuple[str, int, int]]:
    """Enforce the minimum silence / segment rules until the tiling is stable.

    The two rules are applied repeatedly because either one can expose a new
    short neighbor (e.g. an isolated pop splits one long silence into two):

    * A segment shorter than the minimum is reclassified as silence.  When it
      sat between two real segments those become adjacent and join; when it was
      an isolated blip inside silence, the surrounding silence simply heals.
    * A short *interior* silence flanked by segments is bridged so the two
      segments merge across it.  Leading and trailing silence is never removed.
    """
    work = _collapse([list(run) for run in runs])
    changed = True
    while changed:
        changed = False

        # Rule 1: demote every sub-minimum segment to silence, then collapse.
        for run in work:
            if run[0] == "segment" and (run[2] - run[1]) < min_segment_samples:
                run[0] = "silence"
                changed = True
        if changed:
            work = _collapse(work)
            continue

        # Rule 2: bridge short interior silences that separate two segments.
        for idx in range(1, len(work) - 1):
            kind, start, end = work[idx]
            if (
                kind == "silence"
                and work[idx - 1][0] == "segment"
                and work[idx + 1][0] == "segment"
                and (end - start) < min_silence_samples
            ):
                work[idx][0] = "segment"
                changed = True
                break
        if changed:
            work = _collapse(work)

    return [(k, s, e) for (k, s, e) in work]


def _estimate_noise_floor(rms: np.ndarray, noise_percentile: float) -> float:
    """Estimate the room-tone RMS from the quietest frames.

    Primary statistic: the median RMS of the lowest ``noise_percentile``
    percent of frames.  If the material has no genuinely quiet frames (the
    quietest frame is nearly as loud as the loudest) that statistic would
    measure content rather than floor, so it is anchored 12 dB below the
    quietest frame instead, keeping the content above the derived threshold.
    """
    ordered = np.sort(rms)
    if ordered.size == 0:
        return 0.0
    quiet_count = max(1, int(round(ordered.size * noise_percentile / 100.0)))
    floor = float(np.median(ordered[:quiet_count]))
    quietest = float(ordered[0])
    peak = float(ordered[-1])
    if not np.isfinite(floor) or not np.isfinite(peak):
        return quietest if np.isfinite(quietest) else 0.0

    # Room tone shows natural frame-to-frame spread (peak/quietest well above
    # ~1.5); a ratio below that means every frame is effectively content.
    has_quiet_frames = quietest > 0.0 and (peak / max(quietest, _EPSILON)) >= 1.5
    if not has_quiet_frames:
        floor = min(floor, quietest / (10.0 ** (12.0 / 20.0)))
    return max(floor, 0.0)


def detect(audio: AudioData, params: AnalysisParams) -> DetectionResult:
    """Run the full detection pipeline over one validated mono signal."""
    samples = audio.samples
    total = samples.shape[0]
    if total <= 0:
        raise ValueError("cannot analyze empty audio")
    if not np.all(np.isfinite(samples)):
        raise ValueError("audio contains non-finite samples (NaN/Inf)")

    rms, edges = frame_rms(samples, params.frame_samples)
    if not np.all(np.isfinite(rms)):
        raise ValueError("frame energy computation produced non-finite values")

    # Noise floor from the quietest frames; thresholds follow the user margin
    # plus hysteresis, never a fixed absolute level.
    noise_floor = _estimate_noise_floor(rms, params.noise_percentile)
    enter_threshold = max(noise_floor * (10.0 ** (params.margin_db / 20.0)), _EPSILON)
    exit_threshold = max(
        noise_floor * (10.0 ** ((params.margin_db - params.hysteresis_db) / 20.0)),
        _EPSILON,
    )
    exit_threshold = min(exit_threshold, enter_threshold)

    if float(np.max(rms)) <= _EPSILON:
        runs = [("silence", 0, total)]
    else:
        labels = _classify_frames(rms, enter_threshold, exit_threshold)
        runs = _runs_from_labels(labels, edges, samples, enter_threshold, exit_threshold)
        runs = _resolve_minimum_durations(
            runs, params.min_silence_samples, params.min_segment_samples
        )

    regions = tuple(Region(kind=kind, start_sample=start, end_sample=end) for kind, start, end in runs)
    _assert_tiling(regions, total)
    return DetectionResult(
        regions=regions,
        noise_floor_rms=noise_floor,
        enter_threshold_rms=enter_threshold,
        exit_threshold_rms=exit_threshold,
        frame_count=int(rms.shape[0]),
        total_samples=total,
    )


def _assert_tiling(regions: tuple[Region, ...], total: int) -> None:
    """Guard the structural invariants every result must satisfy."""
    if not regions:
        raise AssertionError("detection produced no regions")
    cursor = 0
    previous_kind = None
    for region in regions:
        if region.is_empty():
            raise AssertionError("zero-length region generated")
        if region.start_sample != cursor:
            raise AssertionError("regions do not tile the signal contiguously")
        if region.kind == previous_kind:
            raise AssertionError("adjacent regions share a kind")
        cursor = region.end_sample
        previous_kind = region.kind
    if cursor != total:
        raise AssertionError("regions do not cover the full signal")
