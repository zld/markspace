"""Streaming frame energy detection with an adaptive noise floor.

A bounded history of quiet frames drives the floor. It is frozen while an
event is active so weak events stay detectable next to loud ones.
"""
from __future__ import annotations

import math
from collections import deque
from dataclasses import dataclass, field

import numpy as np

_EPSILON = 1e-12
_MIN_DB = -120.0
_SEED_FRAMES = 4

def amplitude_to_db(value: float) -> float:
    """Convert a linear amplitude to dBFS, clamped at -120 dB."""
    return max(_MIN_DB, 20.0 * math.log10(max(float(value), _EPSILON)))

def db_to_amplitude(db: float) -> float:
    return 10.0 ** (db / 20.0)

@dataclass(frozen=True)
class DetectorConfig:
    frame_ms: float = 10.0
    on_db: float = 4.0
    off_db: float = 2.0
    min_silence_ms: float = 120.0
    attack_frames: int = 2
    hold_frames: int = 2
    noise_window_frames: int = 300
    noise_percentile: float = 20.0
    noise_adapt_rate: float = 0.2

    def validate(self, sample_rate: int) -> None:
        if sample_rate <= 0:
            raise ValueError(f"invalid sample rate {sample_rate}")
        if not math.isfinite(self.frame_ms) or self.frame_ms <= 0:
            raise ValueError("frame_ms must be a positive finite number")
        if self.frame_samples(sample_rate) < 1:
            raise ValueError("frame_ms is too small for this sample rate")
        if not (math.isfinite(self.on_db) and math.isfinite(self.off_db)):
            raise ValueError("on_db and off_db must be finite")
        if self.on_db <= 0 or self.off_db <= 0:
            raise ValueError("on_db and off_db must be greater than 0")
        if self.off_db >= self.on_db:
            raise ValueError("off_db must be smaller than on_db")
        if not math.isfinite(self.min_silence_ms) or self.min_silence_ms < 0:
            raise ValueError("min_silence_ms must be a non-negative number")
        if self.attack_frames < 1:
            raise ValueError("attack_frames must be at least 1")
        if self.hold_frames < 0:
            raise ValueError("hold_frames must be non-negative")
        if self.noise_window_frames < 3:
            raise ValueError("noise_window_frames must be at least 3")
        if not (0.0 <= self.noise_percentile <= 100.0):
            raise ValueError("noise_percentile must be within [0, 100]")
        if not (0.0 < self.noise_adapt_rate <= 1.0):
            raise ValueError("noise_adapt_rate must be in (0, 1]")

    def frame_samples(self, sample_rate: int) -> int:
        return max(1, int(round(self.frame_ms * sample_rate / 1000.0)))

    def hop_samples(self, sample_rate: int) -> int:
        return max(1, self.frame_samples(sample_rate) // 2)

    def min_silence_samples(self, sample_rate: int) -> int:
        return int(round(self.min_silence_ms * sample_rate / 1000.0))

@dataclass(frozen=True)
class DetectedEvent:
    """Raw event with left-closed, right-open sample coordinates."""

    start_sample: int
    end_sample: int
    peak: float
    rms: float
    noise_floor_db: float
    frame_count: int = 1

@dataclass
class DetectionResult:
    events: list[DetectedEvent] = field(default_factory=list)
    frame_count: int = 0
    total_samples: int = 0
    noise_floor_db: float = _MIN_DB

@dataclass
class _VoiceState:
    active: bool = False
    attack_run: int = 0
    quiet_run: int = 0
    start_frame: int = 0
    last_active_frame: int = 0
    peak: float = 0.0
    energy_sum: float = 0.0
    frames: int = 0

    def reset(self) -> None:
        self.active = False
        self.attack_run = 0
        self.quiet_run = 0
        self.peak = 0.0
        self.energy_sum = 0.0
        self.frames = 0

class FrameEnergyDetector:
    """Incremental detector; memory is bounded by its configured window."""

    def __init__(self, sample_rate: int, config: DetectorConfig) -> None:
        config.validate(sample_rate)
        self.sample_rate = sample_rate
        self.config = config
        self.frame = config.frame_samples(sample_rate)
        self.hop = config.hop_samples(sample_rate)
        self.min_silence = config.min_silence_samples(sample_rate)
        self._buffer = np.empty(0, dtype=np.float32)
        self._buffer_start = 0
        self._total_pushed = 0
        self._next_index = 0
        self._state = _VoiceState()
        self._events: list[DetectedEvent] = []
        self._noise_window: deque[float] = deque(
            maxlen=config.noise_window_frames
        )
        self._noise_floor = db_to_amplitude(_MIN_DB)
        self._seed_frames: list[tuple[int, float, float]] = []
        self._seeded = False

    @property
    def noise_floor_db(self) -> float:
        return amplitude_to_db(self._noise_floor)
    def push(self, block: np.ndarray) -> None:
        """Feed int16 or float samples shaped ``(frames, channels)``."""
        block = np.asarray(block)
        if block.ndim not in (1, 2) or (block.ndim == 2 and block.shape[1] < 1):
            raise ValueError("expected samples shaped (frames, channels)")
        if np.issubdtype(block.dtype, np.integer):
            block = block.astype(np.float64) / 32768.0
        if block.ndim == 2:
            mono = block.mean(axis=1, dtype=np.float64)
        else:
            mono = block
        self.push_mono(mono.astype(np.float32, copy=False))

    def push_mono(self, samples: np.ndarray) -> None:
        samples = np.asarray(samples, dtype=np.float32).reshape(-1)
        if samples.size == 0:
            return
        if self._buffer.size:
            self._buffer = np.concatenate((self._buffer, samples))
        else:
            self._buffer = samples
        self._total_pushed += int(samples.size)
        self._drain()

    def finish(self, total_samples: int) -> DetectionResult:
        if total_samples != self._total_pushed:
            raise ValueError(
                f"finish() expected {total_samples} samples, "
                f"received {self._total_pushed}"
            )
        if self._buffer_start < total_samples:
            padded = np.concatenate(
                (self._buffer, np.zeros(self.frame, dtype=np.float32))
            )
            position = 0
            while self._buffer_start + position < total_samples:
                valid = min(self.frame, total_samples - self._buffer_start - position)
                chunk = padded[position : position + valid]
                self._process_frame(
                    chunk,
                    start=self._buffer_start + position,
                    valid=valid,
                )
                position += self.hop
        if not self._seeded:
            self._seed_and_flush()
        self._close_active(total_samples)
        return DetectionResult(
            events=self._events,
            frame_count=self._next_index,
            total_samples=total_samples,
            noise_floor_db=self.noise_floor_db,
        )

    def _drain(self) -> None:
        while self._buffer.size >= self.frame:
            chunk = self._buffer[: self.frame]
            self._process_frame(chunk, start=self._buffer_start, valid=self.frame)
            self._buffer = self._buffer[self.hop :]
            self._buffer_start += self.hop

    def _process_frame(self, samples: np.ndarray, start: int, valid: int) -> None:
        samples = samples[:valid]
        energy = float(np.mean(np.square(samples, dtype=np.float64)))
        peak = float(np.max(np.abs(samples)))
        rms = math.sqrt(max(energy, 0.0))
        if not self._seeded:
            self._seed_frames.append((start, rms, peak))
            if len(self._seed_frames) < _SEED_FRAMES:
                return
            self._seed_and_flush()
            return
        self._evaluate(start, rms, peak)

    def _seed_and_flush(self) -> None:
        """Seed the floor from the quietest early frame, then replay frames.

        Using the minimum of a few frames keeps a single spike from
        inflating the floor while still giving steady noise a sane start.
        """
        self._noise_floor = max(
            min(rms for _start, rms, _peak in self._seed_frames), _EPSILON
        )
        self._seeded = True
        pending, self._seed_frames = self._seed_frames, []
        for start, rms, peak in pending:
            self._evaluate(start, rms, peak)

    def _evaluate(self, start: int, rms: float, peak: float) -> None:
        self._apply_state(self._next_index, start, rms, peak)
        self._next_index += 1
        self._update_noise(rms)

    def _thresholds(self) -> tuple[float, float]:
        """Return (attack, release) amplitudes for the current floor."""
        floor_db = amplitude_to_db(self._noise_floor)
        return db_to_amplitude(floor_db + self.config.on_db), db_to_amplitude(
            floor_db + self.config.off_db
        )

    def _apply_state(self, index: int, start: int, rms: float, peak: float) -> None:
        state = self._state
        on_amp, off_amp = self._thresholds()
        if not state.active:
            if rms >= on_amp:
                state.attack_run += 1
            else:
                state.attack_run = 0
            if state.attack_run >= self.config.attack_frames:
                state.active = True
                state.start_frame = index - state.attack_run + 1
                state.last_active_frame = index
                state.quiet_run = 0
                state.peak = peak
                state.energy_sum = rms * rms
                state.frames = 1
            return

        state.peak = max(state.peak, peak)
        state.energy_sum += rms * rms
        state.frames += 1
        if rms >= off_amp:
            state.quiet_run = 0
            state.last_active_frame = index
            return
        state.quiet_run += 1
        needed = self._release_frames()
        if state.quiet_run >= needed:
            self._emit_closed_event()
            state.reset()

    def _release_frames(self) -> int:
        # Gaps are merged by duration in :mod:`audiosplit.merge`; here we only
        # hold a short tail so fades and decay are not clipped.
        return max(1, self.config.hold_frames)

    def _emit(self, start: int, end: int) -> None:
        state = self._state
        if end <= start:
            return
        self._events.append(
            DetectedEvent(
                start_sample=start,
                end_sample=end,
                peak=state.peak,
                rms=math.sqrt(state.energy_sum / max(1, state.frames)),
                noise_floor_db=self.noise_floor_db,
                frame_count=state.frames,
            )
        )

    def _emit_closed_event(self) -> None:
        state = self._state
        start = max(0, state.start_frame * self.hop)
        # Keep the tail: hold_frames of silence still belong to the event.
        end = (state.last_active_frame + 1 + self.config.hold_frames) * self.hop
        self._emit(start, end)

    def _close_active(self, total_samples: int) -> None:
        state = self._state
        if not state.active:
            return
        self._emit(max(0, state.start_frame * self.hop), total_samples)
        state.reset()

    def _update_noise(self, rms: float) -> None:
        if self._state.active:
            return  # frozen for the whole event
        _, off_amp = self._thresholds()
        if rms >= off_amp:
            return  # attack/deadband frames never feed the floor
        self._noise_window.append(rms)
        if len(self._noise_window) < 3:
            measured = min(self._noise_window)
        else:
            measured = float(
                np.percentile(
                    np.fromiter(self._noise_window, dtype=np.float64),
                    self.config.noise_percentile,
                )
            )
        measured = max(measured, _EPSILON)
        if measured <= self._noise_floor:
            self._noise_floor = measured
        else:
            rate = self.config.noise_adapt_rate
            self._noise_floor += rate * (measured - self._noise_floor)
