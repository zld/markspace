"""Offline rendering, resampling and automation consistency."""

from __future__ import annotations

import numpy as np
import pytest

from mixforge.automation import AutomationCurve, db_to_linear, fade_amplitude
from mixforge.errors import ConfigError
from mixforge.model import AutomationSpec, GainPoint
from mixforge.render import render_offline
from mixforge.resample import output_length, resample_audio
from mixforge.stats import compare_samples

from conftest import base_spec, make_project


def curve_linear() -> AutomationCurve:
    return AutomationCurve.from_spec(
        AutomationSpec("linear", (GainPoint(10, 0.0), GainPoint(20, -6.0)))
    )


@pytest.mark.parametrize("block_size", [1, 7, 128, 1024, 65_536])
def test_offline_audio_is_identical_across_block_sizes(fixture_dir, block_size):
    project = make_project(fixture_dir.parent, base_spec())
    reference, _ = render_offline(project, block_size=1024)
    audio, _ = render_offline(project, block_size=block_size)
    assert audio.shape == (24_000, 2)
    assert compare_samples(reference, audio) == (0.0, None)


def test_automation_is_sampled_at_absolute_timeline(fixture_dir):
    project = make_project(fixture_dir.parent, base_spec())
    audio_small, summary = render_offline(project, block_size=256)
    audio_large, _ = render_offline(project, block_size=4096)
    assert compare_samples(audio_small, audio_large) == (0.0, None)
    # Keyframes are declared in absolute project frames, not per block.
    rows = {row["frame"]: row for row in summary.automation_samples
            if row["track"] == "ramp" and "keyframe" in row["roles"]}
    assert rows[0]["gain_db"] == pytest.approx(-12.0)
    assert rows[5000]["gain_db"] == pytest.approx(0.0)
    assert rows[13000]["gain_db"] == pytest.approx(-6.0)


def test_linear_and_step_interpolation_and_held_edges():
    linear = curve_linear()
    assert linear.gain_db_at(np.array([0, 15, 30])).tolist() == pytest.approx([0.0, -3.0, -6.0])
    step = AutomationCurve.from_spec(
        AutomationSpec("step", (GainPoint(10, 0.0), GainPoint(20, -6.0)))
    )
    assert step.gain_db_at(np.array([0, 10, 19, 20, 25])).tolist() == pytest.approx(
        [0.0, 0.0, 0.0, -6.0, -6.0]
    )


def test_automation_rejects_duplicates_and_non_finite_gains():
    duplicate = AutomationSpec("linear", (GainPoint(5, 0.0), GainPoint(5, -1.0)))
    with pytest.raises(ConfigError):
        AutomationCurve.from_spec(duplicate)
    with pytest.raises(ConfigError):
        AutomationCurve.from_spec(AutomationSpec("linear", (GainPoint(5, float("nan")),)))


def test_fade_envelope_edges_and_overlong_rejected():
    envelope = fade_amplitude(100, 10, 10, np.array([0, 5, 9, 50, 90, 99]))
    assert envelope.tolist() == pytest.approx([0.0, 0.5, 0.9, 1.0, 0.9, 0.0])
    with pytest.raises(ConfigError):
        fade_amplitude(10, 8, 8, np.array([0]))


def test_resample_lengths_and_special_inputs():
    x = np.array([[0.0], [0.5], [1.0]])
    assert resample_audio(x, 2, 4).ravel().tolist() == pytest.approx(
        [0.0, 0.25, 0.5, 0.75, 1.0, 1.0]
    )
    assert resample_audio(x, 4, 2).ravel().tolist() == pytest.approx([0.0, 1.0])
    assert resample_audio(np.empty((0, 2)), 48_000, 44_100).shape == (0, 2)
    assert resample_audio(np.empty((3, 2)), 48_000, 44_100).shape == (3, 2)
    assert resample_audio(np.array([[0.25]]), 44_100, 48_000).shape[0] == 1
    assert output_length(1, 48_000, 8_000) == 0
    assert output_length(0, 48_000, 48_000) == 0


def test_resample_rejects_non_finite_input():
    from mixforge.errors import ProcessingError

    with pytest.raises(ProcessingError):
        resample_audio(np.array([[np.nan], [0.0]]), 48_000, 48_000)
    with pytest.raises(ProcessingError):
        resample_audio(np.array([[np.inf], [0.0]]), 48_000, 48_000)


def test_db_helper_matches_reference():
    assert float(db_to_linear(-6.0)) == pytest.approx(10 ** (-6 / 20))


def test_track_stats_and_master_clip_domain(fixture_dir):
    project = make_project(fixture_dir.parent, base_spec(master={"limiter": False}))
    _, summary = render_offline(project, block_size=1024)
    payload = summary.as_dict()
    tone = next(track for track in payload["tracks"] if track["id"] == "tone")
    assert tone["samples"] == 24_000 * 2
    assert tone["clip_count"] > 0
    assert tone["clip_domain"] == "track_post_gain"
    assert payload["master"]["clip_domain"] == "master_pre_limit"
    assert payload["master"]["clip_count"] >= tone["clip_count"] - 1
    muted = next(track for track in payload["tracks"] if track["id"] == "silence")
    assert muted["peak"] == 0.0
    assert muted["clip_count"] == 0
