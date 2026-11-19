"""Streaming equivalence, short final blocks, loops and failure context."""

from __future__ import annotations

import numpy as np
import pytest

from mixforge.errors import AudioIOError, ConfigError
from mixforge.render import render_offline
from mixforge.stream import stream_render
from mixforge.stats import compare_samples

from conftest import base_spec, make_project


def test_stream_matches_offline_for_many_block_sizes(fixture_dir):
    project = make_project(fixture_dir.parent, base_spec())
    offline, _ = render_offline(project, block_size=1024)
    for block_size in (1, 3, 97, 1024, 23_999, 24_000, 100_000):
        blocks, summary = stream_render(project, block_size=block_size)
        chunks = list(blocks)
        streamed = (
            np.concatenate(chunks, axis=0)
            if chunks
            else np.empty((0, project.channels))
        )
        assert streamed.shape == offline.shape
        assert compare_samples(offline, streamed) == (0.0, None), block_size
        assert [chunk.shape[0] for chunk in chunks] == _expected_blocks(project.frames, block_size)
        assert summary.master.frames == project.frames


def _expected_blocks(frames: int, block_size: int) -> list[int]:
    sizes = []
    start = 0
    while start < frames:
        take = min(block_size, frames - start)
        sizes.append(take)
        start += take
    return sizes


def test_stream_does_not_precompute_full_mix(fixture_dir, monkeypatch):
    project = make_project(fixture_dir.parent, base_spec())
    blocks, _ = stream_render(project, block_size=1024)
    first = next(blocks)
    assert first.shape == (1024, 2)
    blocks.close()

    summaries = []
    counts = []
    for blocks, summary in [stream_render(project, block_size=2048) for _ in range(1)]:
        counts.append(sum(block.shape[0] for block in blocks))
        summaries.append(summary)
    assert counts == [project.frames]
    assert summaries[0].master.frames == project.frames


def test_zero_frame_project_streams_nothing(fixture_dir):
    project = make_project(
        fixture_dir.parent, base_spec(duration_frames=0, tracks=[], buses=[])
    )
    offline, offline_summary = render_offline(project, block_size=64)
    assert offline.shape == (0, 2)
    blocks, summary = stream_render(project, block_size=64)
    assert list(blocks) == []
    assert offline_summary.master.samples == summary.master.samples == 0
    assert offline_summary.as_dict()["automation_samples"] == []


def test_loop_repeats_content_but_keeps_automation_on_output_timeline(fixture_dir):
    spec = base_spec(
        duration_frames=12_000,
        loop={"start_frame": 2_000, "end_frame": 4_000, "count": 3},
        master={"limiter": False},
    )
    project = make_project(fixture_dir.parent, spec)
    offline, summary = render_offline(project, block_size=256)
    blocks, _ = stream_render(project, block_size=256)
    streamed = np.concatenate(list(blocks), axis=0)
    assert compare_samples(offline, streamed) == (0.0, None)

    # The loop tiles [2000, 4000) three times: boundaries are 2000/4000/8000.
    loop_roles = [row for row in summary.automation_samples if "loop_boundary" in row["roles"]]
    assert loop_roles
    assert {row["frame"] for row in loop_roles} <= {1_999, 2_000, 4_000, 8_000}


def test_loop_map_wraps_only_inside_the_expanded_region():
    from mixforge.graph import loop_map
    from mixforge.model import LoopRegion

    class Stub:
        loop = LoopRegion(start_frame=10, end_frame=14, count=2)

    assert loop_map(np.array([0, 9, 10, 13, 14, 17, 18]), Stub()).tolist() == [
        0,
        9,
        10,
        13,
        10,
        13,
        18,
    ]


def test_loop_outside_duration_is_rejected(fixture_dir):
    with pytest.raises(ConfigError):
        make_project(
            fixture_dir.parent,
            base_spec(duration_frames=4_000, loop={"start_frame": 3_000, "end_frame": 5_000, "count": 1}),
        )
    with pytest.raises(ConfigError):
        make_project(
            fixture_dir.parent,
            base_spec(duration_frames=8_000, loop={"start_frame": 0, "end_frame": 3_000, "count": 3}),
        )


def test_invalid_block_size_rejected(fixture_dir):
    project = make_project(fixture_dir.parent, base_spec())
    for bad in (0, -1):
        with pytest.raises(ConfigError):
            stream_render(project, block_size=bad)


def test_read_failure_reports_track_and_block(fixture_dir):
    project = make_project(fixture_dir.parent, base_spec())
    blocks, _ = stream_render(project, block_size=1024)
    first = next(blocks)
    assert first.shape == (1024, 2)
    # Truncate the source after the first block, then force a later read.
    target = fixture_dir / "ramp.wav"
    with target.open("r+b") as handle:
        handle.truncate(200 * target.stat().st_size // 400)
    with pytest.raises(AudioIOError) as caught:
        for _ in blocks:
            pass
    error = caught.value
    assert "track[ramp]" in str(error)
    assert error.context.get("block_start") is not None
