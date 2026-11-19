"""End-to-end simulation of trace, estimator, policy, and buffer."""

from __future__ import annotations

import math
from dataclasses import asdict, dataclass, field

from .buffer import BufferModel
from .errors import SimulationError
from .estimator import BandwidthEstimator
from .ladder import Ladder
from .policy import AbrPolicy
from .trace import NetworkTrace, generate_trace

# Loss bursts throttle delivery and inflate the request RTT.
LOSS_THROUGHPUT_PENALTY = 0.75
LOSS_RTT_PENALTY = 2.0


@dataclass(frozen=True)
class SegmentRecord:
    """Everything observed while one segment was selected and downloaded."""

    segment: int
    tier: int
    estimated_kbps: float
    selected_kbps: float
    throughput_kbps: float
    rtt_ms: float
    loss_probability: float
    download_seconds: float
    buffer_before_seconds: float
    buffer_after_seconds: float
    stall_seconds: float
    reason: str
    switched: bool

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass(frozen=True)
class SimulationSummary:
    segments: int
    switch_count: int
    stall_seconds: float
    mean_estimated_kbps: float
    mean_selected_kbps: float
    mean_buffer_seconds: float
    mean_download_seconds: float
    final_tier: int
    final_bitrate_kbps: float
    final_buffer_seconds: float

    def to_dict(self) -> dict:
        return asdict(self)


@dataclass
class SimulationResult:
    records: list[SegmentRecord] = field(default_factory=list)
    summary: SimulationSummary | None = None

    def to_dict(self) -> dict:
        if self.summary is None:
            raise SimulationError("simulation result has no summary")
        return {
            "summary": self.summary.to_dict(),
            "segments": [record.to_dict() for record in self.records],
        }


def _validate_record(record: SegmentRecord) -> None:
    if not math.isfinite(record.throughput_kbps) or record.throughput_kbps <= 0:
        raise SimulationError(
            f"segment {record.segment} produced invalid throughput {record.throughput_kbps!r}"
        )
    if not math.isfinite(record.selected_kbps) or record.selected_kbps <= 0:
        raise SimulationError(
            f"segment {record.segment} selected invalid bitrate {record.selected_kbps!r}"
        )


def simulate(
    trace: NetworkTrace,
    ladder: Ladder,
    policy: AbrPolicy,
    buffer_model: BufferModel,
    estimator: BandwidthEstimator | None = None,
) -> SimulationResult:
    """Run the trace through the engine and return per-segment records.

    Each segment is decided from the *previous* observations, so the first
    segment sees only the estimator's cold-start fallback. Effective throughput
    and RTT reflect loss bursts. The estimator is then updated with the
    delivered throughput, feeding the next decision.
    """
    if len(trace) == 0:
        raise SimulationError("trace contains no segments")
    if estimator is None:
        estimator = BandwidthEstimator()

    records: list[SegmentRecord] = []
    switch_count = 0
    previous_tier: int | None = None

    for index, sample in enumerate(trace.samples):
        if not math.isfinite(sample.throughput_kbps) or sample.throughput_kbps <= 0:
            raise SimulationError(
                f"trace segment {index + 1} has invalid throughput {sample.throughput_kbps!r}"
            )
        if not math.isfinite(sample.rtt_ms) or sample.rtt_ms < 0:
            raise SimulationError(f"trace segment {index + 1} has invalid RTT {sample.rtt_ms!r}")
        if not 0.0 <= sample.loss_probability <= 1.0:
            raise SimulationError(
                f"trace segment {index + 1} has invalid loss probability "
                f"{sample.loss_probability!r}"
            )

        penalty = LOSS_THROUGHPUT_PENALTY if sample.loss_burst else 1.0
        effective_throughput = sample.throughput_kbps * (1.0 - sample.loss_probability * penalty)
        if effective_throughput <= 0:
            raise SimulationError(
                f"trace segment {index + 1} delivered no effective throughput"
            )
        effective_rtt = sample.rtt_ms * (1.0 + LOSS_RTT_PENALTY * sample.loss_probability)

        estimated = estimator.available_kbps()
        decision = policy.select(estimated, buffer_model.level_seconds)
        state = buffer_model.consume(
            decision.bitrate_kbps,
            ladder.segment_seconds,
            effective_throughput,
            effective_rtt,
        )
        estimator.update(effective_throughput)

        if previous_tier is not None and decision.tier != previous_tier:
            switch_count += 1
        record = SegmentRecord(
            segment=index + 1,
            tier=decision.tier,
            estimated_kbps=estimated,
            selected_kbps=decision.bitrate_kbps,
            throughput_kbps=effective_throughput,
            rtt_ms=effective_rtt,
            loss_probability=sample.loss_probability,
            download_seconds=state.download_seconds,
            buffer_before_seconds=state.before_seconds,
            buffer_after_seconds=state.after_seconds,
            stall_seconds=state.stall_seconds,
            reason=decision.reason,
            switched=previous_tier is not None and decision.tier != previous_tier,
        )
        _validate_record(record)
        records.append(record)
        previous_tier = decision.tier

    count = len(records)
    summary = SimulationSummary(
        segments=count,
        switch_count=switch_count,
        stall_seconds=buffer_model.total_stall_seconds,
        mean_estimated_kbps=sum(r.estimated_kbps for r in records) / count,
        mean_selected_kbps=sum(r.selected_kbps for r in records) / count,
        mean_buffer_seconds=sum(r.buffer_after_seconds for r in records) / count,
        mean_download_seconds=sum(r.download_seconds for r in records) / count,
        final_tier=records[-1].tier,
        final_bitrate_kbps=records[-1].selected_kbps,
        final_buffer_seconds=records[-1].buffer_after_seconds,
    )
    return SimulationResult(records=records, summary=summary)


def simulate_from_parameters(
    segments: int,
    seed: int,
    ladder: Ladder,
    initial_buffer_seconds: float,
    buffer_capacity_seconds: float,
    policy: AbrPolicy | None = None,
    estimator: BandwidthEstimator | None = None,
    buffer_model: BufferModel | None = None,
    **trace_kwargs,
) -> SimulationResult:
    """Generate a trace from parameters and run the simulation.

    Supplying ``buffer_model`` lets the caller validate buffer configuration
    before any trace work happens; otherwise one is built from the limits.
    """
    trace = generate_trace(segments=segments, seed=seed, **trace_kwargs)
    if policy is None:
        policy = AbrPolicy(ladder)
    if buffer_model is None:
        buffer_model = BufferModel(buffer_capacity_seconds, initial_buffer_seconds)
    return simulate(trace, ladder, policy, buffer_model, estimator)
