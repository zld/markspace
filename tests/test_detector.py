"""Unit tests for the adaptive detector and its parameter validation."""
from __future__ import annotations

import time

import numpy as np
import pytest

from audiosplit.detector import detect
from audiosplit.models import AudioData, DetectParams, ParamError

SR = 48000


def _audio(samples: np.ndarray, sr: int = SR, channels: int = 1) -> AudioData:
    arr = np.asarray(samples, dtype=np.float32)
    if channels > 1:
        arr = np.column_stack([arr, 0.5 * arr])
    else:
        arr = arr.reshape(-1, 1)
    return AudioData("mem", arr, sr, channels, 2)


def _noise(rng: np.random.Generator, seconds: float, amp: float) -> np.ndarray:
    return rng.normal(0, amp, int(SR * seconds)).astype(np.float32)


def _tone(rng: np.random.Generator, seconds: float, amp: float,
          freq: float = 300.0) -> np.ndarray:
    n = int(SR * seconds)
    t = np.arange(n) / SR
    env = np.hanning(n)
    return (amp * np.sin(2 * np.pi * freq * t) * env
            + rng.normal(0, amp * 0.05, n) * env).astype(np.float32)


@pytest.fixture
def rng() -> np.random.Generator:
    return np.random.default_rng(7)


def test_two_bursts_separated_by_long_silence(rng):
    signal = np.concatenate([
        _noise(rng, 0.3, 0.004), _tone(rng, 0.8, 0.3),
        _noise(rng, 0.5, 0.004), _tone(rng, 0.6, 0.08),
        _noise(rng, 0.3, 0.004),
    ])
    result = detect(_audio(signal), DetectParams())
    assert len(result.segments) == 2
    first, second = result.segments
    # both segments sit inside the file and cover the burst neighbourhoods
    assert 0 <= first.start_sample < first.end_sample <= result.total_samples
    assert first.end_sample < second.start_sample
    gap_ms = (second.start_sample - first.end_sample) / SR * 1000
    assert gap_ms > 120  # the configured min-silence split point


def test_short_silence_merges_segments(rng):
    # 50 ms of low-level gap: below the 120 ms merge threshold, so the two
    # bursts survive state-machine closure and come back as one segment.
    signal = np.concatenate([
        _tone(rng, 0.4, 0.25), _noise(rng, 0.05, 0.004), _tone(rng, 0.4, 0.25),
    ])
    result = detect(_audio(signal), DetectParams(min_silence_ms=120))
    assert len(result.segments) == 1


def test_all_silence_yields_no_segments_and_full_silence(rng):
    result = detect(_audio(_noise(rng, 1.0, 0.001)), DetectParams())
    assert result.segments == []
    assert len(result.silences) == 1
    assert result.silences[0].start_sample == 0
    assert result.silences[0].end_sample == result.total_samples


def test_constant_loud_signal_covers_whole_clip():
    result = detect(_audio(np.full(SR, 0.5, np.float32)), DetectParams())
    assert len(result.segments) == 1
    seg = result.segments[0]
    assert seg.start_sample == 0 and seg.end_sample == SR
    assert result.silences == []


def test_fade_in_and_fade_out_keep_tails(rng):
    n = int(SR * 1.2)
    body = np.full(n, 0.35, np.float32)
    fade = int(SR * 0.25)
    body[:fade] *= np.linspace(0, 1, fade)
    body[-fade:] *= np.linspace(1, 0, fade)
    body += rng.normal(0, 0.002, n)
    result = detect(_audio(body.astype(np.float32)), DetectParams())
    assert len(result.segments) == 1
    seg = result.segments[0]
    # backtracking reaches well into both fade ramps instead of starting at
    # the loud body; padding then extends the retained region toward edges
    assert seg.start_sample < int(SR * 0.2)
    assert seg.end_sample > int(SR * 1.0)
    assert 0 <= seg.start_sample and seg.end_sample <= result.total_samples


def test_boundaries_are_half_open_integers(rng):
    signal = np.concatenate([
        _noise(rng, 0.2, 0.004), _tone(rng, 0.5, 0.3), _noise(rng, 0.2, 0.004),
    ])
    result = detect(_audio(signal), DetectParams())
    for seg in result.segments:
        assert isinstance(seg.start_sample, int)
        assert isinstance(seg.end_sample, int)
        assert 0 <= seg.start_sample < seg.end_sample <= result.total_samples
    # silences and segments partition the whole sample range without overlap
    covered = sum(s.length() for s in result.segments) + sum(
        z.length() for z in result.silences
    )
    assert covered == result.total_samples


def test_padding_is_clamped_to_file_bounds(rng):
    signal = np.concatenate([_tone(rng, 0.4, 0.3), _noise(rng, 0.1, 0.004)])
    result = detect(_audio(signal), DetectParams(pad_ms=50))
    assert result.segments[0].start_sample >= 0
    assert result.segments[-1].end_sample <= result.total_samples


def test_stereo_uses_combined_channel_energy(rng):
    # quiet bed with an event carried only by the right channel
    bed = _noise(rng, 1.0, 0.003)
    stereo = np.column_stack([bed, bed.copy()])
    start, end = int(SR * 0.3), int(SR * 0.6)
    stereo[start:end, 1] = 0.3
    result = detect(AudioData("st", stereo.astype(np.float32), SR, 2, 2),
                    DetectParams())
    assert len(result.segments) == 1
    seg = result.segments[0]
    # detected region overlaps the right-channel event
    assert seg.start_sample < end and seg.end_sample > start


def test_param_validation_rejects_out_of_range():
    with pytest.raises(ParamError):
        DetectParams(frame_ms=0.0).validate()
    with pytest.raises(ParamError):
        DetectParams(noise_margin_db=100.0).validate()
    with pytest.raises(ParamError):
        DetectParams(min_silence_ms=-1.0).validate()
    with pytest.raises(ParamError):
        DetectParams(frame_ms=float("nan")).validate()


def test_min_segment_drops_short_runs(rng):
    # a single 10 ms click against a quiet bed, below the 30 ms minimum
    signal = _noise(rng, 0.5, 0.003)
    signal[1000:1480] = 0.4
    result = detect(_audio(signal), DetectParams(min_segment_ms=30))
    assert all(seg.length() >= int(SR * 0.03) for seg in result.segments)


def test_realtime_performance_budget(rng):
    seconds = 60
    signal = rng.normal(0, 0.01, SR * seconds).astype(np.float32)
    for i in range(5):
        start = i * SR * 10 + SR
        signal[start: start + SR] = 0.3
    started = time.perf_counter()
    detect(_audio(signal), DetectParams())
    elapsed = time.perf_counter() - started
    assert seconds / elapsed >= 5.0
