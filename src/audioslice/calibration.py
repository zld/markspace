"""Robust noise-floor estimation and adaptive dual thresholds.

The detector must not rely on a fixed amplitude threshold: quiet speech sits
just above a low noise floor, while the floor itself varies per recording.
We therefore estimate the noise level from *candidate silent windows*
(lowest-RMS windows of the file) and derive two thresholds:

* ``on_threshold``  — RMS at/above which a window counts as active content;
* ``off_threshold`` — RMS at/below which an active run returns to silence.

The gap between them (configurable hysteresis in dB) prevents chattering
when low-level noise wobbles around a single threshold.

Estimation uses a bounded reservoir, so memory never grows with file length.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from .errors import ConfigError

#: Maximum number of window RMS values retained while scanning a file.
DEFAULT_RESERVOIR = 4096
#: Fraction of the quietest windows treated as representative silence.
NOISE_QUANTILE = 0.2
#: Floor applied to an all-zero / near-zero file so 10*log10 stays defined.
RMS_FLOOR = 1e-12


@dataclass(frozen=True)
class Calibration:
    """Result of estimating the noise floor for one recording."""

    noise_rms: float
    on_threshold: float
    off_threshold: float
    window_count: int

    def as_dict(self) -> dict[str, float]:
        return {
            "noise_rms": self.noise_rms,
            "on_threshold": self.on_threshold,
            "off_threshold": self.off_threshold,
            "window_count": self.window_count,
        }


def _db_to_gain(db: float) -> float:
    return 10.0 ** (db / 20.0)


class Calibrator:
    """Stream window RMS values in, then derive thresholds once.

    A reservoir keeps the ``DEFAULT_RESERVOIR`` quietest-ish candidates via
    reservoir sampling: every window has an equal chance of being retained,
    so for a mostly-silent file the retained sample is dominated by silent
    windows without ever storing the whole recording.
    """

    def __init__(self, noise_margin_db: float, hysteresis_db: float = 3.0,
                 capacity: int = DEFAULT_RESERVOIR, seed: int = 0) -> None:
        if noise_margin_db <= 0:
            raise ConfigError("noise-margin-db must be positive")
        if hysteresis_db < 0:
            raise ConfigError("hysteresis-db must not be negative")
        if capacity < 4:
            raise ConfigError("calibration reservoir capacity must be >= 4")
        self.noise_margin_db = float(noise_margin_db)
        self.hysteresis_db = float(hysteresis_db)
        self._capacity = capacity
        self._rng = np.random.default_rng(seed)
        self._reservoir = np.empty(capacity, dtype=np.float64)
        self._seen = 0
        self.min_rms: float | None = None
        self.max_rms: float | None = None

    def add(self, window_rms: np.ndarray) -> None:
        """Add one window's RMS (or a vector of window RMS values)."""
        values = np.asarray(window_rms, dtype=np.float64).reshape(-1)
        if values.size:
            self.min_rms = float(values.min()) if self.min_rms is None \
                else min(self.min_rms, float(values.min()))
            self.max_rms = float(values.max()) if self.max_rms is None \
                else max(self.max_rms, float(values.max()))
        for value in values:
            if self._seen < self._capacity:
                self._reservoir[self._seen] = value
            else:
                slot = self._rng.integers(0, self._seen + 1)
                if slot < self._capacity:
                    self._reservoir[slot] = value
            self._seen += 1

    def finalize(self) -> Calibration:
        """Compute the noise floor and dual thresholds from collected windows."""
        if self._seen == 0:
            noise = RMS_FLOOR
        else:
            sample = self._reservoir[: min(self._seen, self._capacity)]
            # Candidate silent windows = the quietest quantile of the sample.
            cutoff = max(1, int(np.ceil(NOISE_QUANTILE * sample.size)))
            quietest = np.partition(sample, cutoff - 1)[:cutoff]
            noise = max(float(np.sqrt(np.mean(quietest ** 2))), RMS_FLOOR)
        on_threshold = noise * _db_to_gain(self.noise_margin_db)
        off_threshold = on_threshold * _db_to_gain(-self.hysteresis_db)
        return Calibration(
            noise_rms=noise,
            on_threshold=on_threshold,
            off_threshold=min(off_threshold, on_threshold),
            window_count=self._seen,
        )

    def homogeneous_label(self, on_threshold: float) -> bool | None:
        """Decide the forced label for a file without usable dynamic range.

        Returns ``True`` (whole file voiced), ``False`` (whole file silent) or
        ``None`` when the dynamic range is wide enough for normal hysteresis.
        """
        if not self._seen or self.min_rms is None or self.max_rms is None:
            return None
        floor = max(self.min_rms, 1e-12)
        spread_db = 20.0 * np.log10(max(self.max_rms, floor) / floor)
        margin = 20.0 * np.log10(on_threshold / floor)
        if np.isfinite(spread_db) and spread_db < margin:
            return self.min_rms > 1e-9
        return None


def calibrate_rms(window_rms: np.ndarray, noise_margin_db: float,
                  hysteresis_db: float = 3.0) -> Calibration:
    """Convenience one-shot calibration over a full array of window RMS values."""
    calibrator = Calibrator(noise_margin_db, hysteresis_db)
    calibrator.add(np.asarray(window_rms, dtype=np.float64))
    return calibrator.finalize()
