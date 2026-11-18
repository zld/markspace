"""Detection boundary tests: thresholds, hysteresis, fades, bursts, errors."""
import wave

import numpy as np
import pytest

from audioslice import audio
from audioslice.audio import AudioError
from audioslice.detector import Detector, DetectorParams
from audioslice.segments import SegmentParams, build_segments

SR = 16_000
NOISE = 0.002


def make_signal(duration_s, events=(), noise=NOISE, seed=1):
    """events: (start_sample, length_samples, amp) abrupt tones."""
    rng = np.random.default_rng(seed)
    x = rng.normal(0.0, noise, int(duration_s * SR)).astype(np.float32)
    for start, length, amp in events:
        t = np.arange(length) / SR
        x[start:start + length] += (amp * np.sin(2 * np.pi * 440 * t)).astype(np.float32)
    return x


def detect(x, params=None, seg_params=None, chunk=None):
    det = Detector(params or DetectorParams(), SR)
    if chunk:
        for i in range(0, x.size, chunk):
            det.feed(x[i:i + chunk])
    else:
        det.feed(x)
    result = det.finish(x.size)
    read = lambda s, e: x[s:e]  # noqa: E731
    return result, build_segments(result, seg_params or SegmentParams(), read)


def test_abrupt_tone_boundaries_are_sample_accurate():
    start, length = 12_345, 4_000
    x = make_signal(2.0, [(start, length, 0.4)])
    _, segs = detect(x)
    assert len(segs) == 1
    assert start - 200 <= segs[0].start_sample <= start
    assert start + length <= segs[0].end_sample <= start + length + 200


def test_low_noise_floor_is_not_activity():
    x = make_signal(3.0)
    result, segs = detect(x)
    assert result.regions == []
    assert segs == []
    assert result.noise_floor == pytest.approx(NOISE, rel=0.5)


def test_weak_signal_below_onset_is_ignored():
    # tone rms ~0.0042 sits between floor (0.002) and onset (0.006)
    x = make_signal(2.0, [(8_000, 8_000, 0.006)])
    _, segs = detect(x)
    assert segs == []


def test_hysteresis_absorbs_threshold_jitter():
    # main tone stays on; dips only fall between offset and onset factors,
    # so the region must not chatter into pieces
    x = make_signal(4.0)
    t = np.arange(16_000) / SR
    tone = 0.010 * np.sin(2 * np.pi * 440 * t)
    for dip in (4_000, 8_000):  # 250-sample dips to a mid-level amplitude
        tone[dip:dip + 250] *= 0.4
    x[16_000:32_000] += tone.astype(np.float32)
    result, _ = detect(x)
    assert len(result.regions) == 1


def test_short_burst_does_not_form_segment():
    burst = int(0.030 * SR)  # 30 ms, below the 120 ms minimum
    x = make_signal(2.0, [(10_000, burst, 0.4)])
    _, segs = detect(x)
    assert segs == []


def test_close_events_merge_distant_events_split():
    ev = int(0.3 * SR)
    x = make_signal(4.0, [(8_000, ev, 0.4), (8_000 + ev + 1_600, ev, 0.4),
                          (56_000, ev, 0.4)])
    _, segs = detect(x)
    assert len(segs) == 2  # 100 ms gap merged, 3 s gap kept apart


def test_fade_in_extends_boundary_before_threshold_crossing():
    sr = SR
    fade = int(0.5 * sr)
    tone_start = 16_000
    length = fade + 4_000
    rng = np.random.default_rng(3)
    x = rng.normal(0.0, NOISE, 3 * sr).astype(np.float32)
    t = np.arange(length) / sr
    env = np.minimum(1.0, np.arange(length) / fade)
    x[tone_start:tone_start + length] += (0.05 * np.sin(2 * np.pi * 220 * t) * env)
    result, segs = detect(x)
    assert len(segs) == 1
    # raw hysteresis would only fire well inside the fade; the trimmed
    # boundary must sit clearly earlier, near the true tone start
    raw_start = result.regions[0].start_frame * result.hop
    assert segs[0].start_sample < raw_start
    assert tone_start - 300 <= segs[0].start_sample <= tone_start + fade // 2


def test_chunked_feeding_matches_single_shot():
    x = make_signal(3.0, [(9_000, 6_000, 0.4), (30_000, 5_000, 0.4)])
    result_one, _ = detect(x)
    result_chunked, _ = detect(x, chunk=1_000)
    np.testing.assert_array_equal(result_one.frame_rms, result_chunked.frame_rms)
    assert result_one.regions == result_chunked.regions


def test_invalid_params_rejected():
    with pytest.raises(ValueError):
        DetectorParams(onset_factor=1.2, offset_factor=1.5).validate()
    with pytest.raises(ValueError):
        DetectorParams(hop_ms=30.0, frame_ms=25.0).validate()


def test_unsupported_bit_depth_rejected(tmp_path):
    path = tmp_path / "wide.wav"
    with wave.open(str(path), "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(3)  # 24-bit is not supported
        wf.setframerate(SR)
        wf.writeframes(b"\x00\x00\x00" * 100)
    with pytest.raises(AudioError, match="bit depth"):
        audio.read_info(path)


def test_empty_and_corrupt_files_rejected(tmp_path):
    empty = tmp_path / "empty.wav"
    with wave.open(str(empty), "wb") as wf:
        wf.setnchannels(1)
        wf.setsampwidth(2)
        wf.setframerate(SR)
        wf.writeframes(b"")
    with pytest.raises(AudioError, match="empty"):
        audio.read_info(empty)
    junk = tmp_path / "junk.wav"
    junk.write_bytes(b"this is not audio")
    with pytest.raises(AudioError):
        audio.read_info(junk)


def test_stereo_mixdown_keeps_sample_indices(tmp_path):
    path = tmp_path / "stereo.wav"
    info = audio.synth_file(path, SR, 1.0, [audio.SynthEvent(0.25, 0.25)],
                            channels=2, seed=5)
    assert info.channels == 2
    assert info.num_samples == SR  # per-channel, not per-frame-value
    mono = np.concatenate(list(audio.iter_mono(path)))
    assert mono.size == SR
