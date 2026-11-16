"""End-to-end pipeline orchestration shared by every CLI entry point.

The flow is fixed:

    EBML reader -> Matroska tracks/blocks -> de-interlacing -> timeline
               -> flat or fragmented MP4 exporter -> structure validation

Both exporters consume the *same* parsed :class:`Timeline`, so a ``--mode
both`` run never re-parses or re-times the input. Limits are applied centrally
so the CLI and any library caller get identical safety guarantees.
"""

from dataclasses import dataclass

from .errors import CorruptMKVError
from .mkv.blocks import deinterlace
from .mkv.segment import MatroskaFile, parse_matroska
from .mp4.flat import FlatBuildResult, export_flat
from .mp4.fragmented import FragmentBuildResult, export_fragmented
from .report import TreeSummary, summarize_tree
from .timeline import Timeline, build_timeline

#: Hard safety limits (requirement: fail safely rather than over-allocate).
MAX_SAMPLES = 200_000
MAX_PACKET_SIZE = 64 * 1024 * 1024      # 64 MiB per block / sample
MAX_NESTING = 16


@dataclass(slots=True)
class PipelineResult:
    """Everything the report layer needs after a successful run."""

    tree: TreeSummary
    mkv: MatroskaFile
    timeline: Timeline
    flat: FlatBuildResult | None
    fragmented: FragmentBuildResult | None


def run_pipeline(input_path: str, mode: str, flat_path: str | None = None,
                 fragmented_path: str | None = None) -> PipelineResult:
    """Parse ``input_path`` once and export the requested MP4 mode(s).

    ``mode`` is one of ``flat``, ``fragmented`` or ``both``. The returned
    result carries exporter summaries for whichever modes ran.
    """
    if mode not in ("flat", "fragmented", "both"):
        raise ValueError(f"unknown export mode {mode!r}")

    tree = summarize_tree(input_path)
    mkv = parse_matroska(
        input_path,
        max_samples=MAX_SAMPLES,
        max_packet_size=MAX_PACKET_SIZE,
    )
    samples = deinterlace(
        mkv,
        max_samples=MAX_SAMPLES,
        max_packet_size=MAX_PACKET_SIZE,
    )
    if not samples:
        raise CorruptMKVError("input produced no samples after de-interlacing")
    timeline = build_timeline(mkv, samples)

    flat_result: FlatBuildResult | None = None
    fragmented_result: FragmentBuildResult | None = None
    if mode in ("flat", "both"):
        if not flat_path:
            raise ValueError("flat mode requires an output path")
        flat_result = export_flat(timeline, flat_path)
    if mode in ("fragmented", "both"):
        if not fragmented_path:
            raise ValueError("fragmented mode requires an output path")
        fragmented_result = export_fragmented(timeline, fragmented_path)

    return PipelineResult(
        tree=tree,
        mkv=mkv,
        timeline=timeline,
        flat=flat_result,
        fragmented=fragmented_result,
    )
