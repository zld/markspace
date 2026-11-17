"""Expand and normalise the scan matrix.

A plan is built once from CLI arguments and is immutable afterwards.  Task
keys are derived from the source hash, the normalised job parameters and the
FFmpeg version, so the same plan on the same machine always produces the same
keys and can therefore be resumed safely.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from .errors import (
    ALLOWED_PRESETS,
    MAX_BITRATE_KBPS,
    MAX_DIMENSION,
    MAX_FRAMES,
    MAX_RESOLUTION_PIXELS,
    MAX_SCAN_COMBINATIONS,
    MAX_WORKERS,
    MIN_BITRATE_KBPS,
    MIN_FRAMES,
    MIN_WORKERS,
    ConfigError,
)

DEFAULT_FPS = 24


@dataclass(frozen=True, order=True)
class Resolution:
    width: int
    height: int

    @property
    def label(self) -> str:
        return f"{self.width}x{self.height}"

    @property
    def pixels(self) -> int:
        return self.width * self.height


@dataclass(frozen=True)
class TargetTier:
    """One unique target bitrate in the ladder request.

    ``aliases`` counts how many *additional* requested entries collapsed into
    this tier (so a bitrate repeated twice has one alias).
    """

    bitrate_kbps: int
    aliases: int = 0

    @property
    def target_bps(self) -> int:
        return self.bitrate_kbps * 1000


@dataclass(frozen=True)
class JobSpec:
    resolution: Resolution
    bitrate_kbps: int
    preset: str

    @property
    def target_bps(self) -> int:
        return self.bitrate_kbps * 1000

    def canonical_params(self) -> dict[str, object]:
        return {
            "width": self.resolution.width,
            "height": self.resolution.height,
            "bitrate_kbps": self.bitrate_kbps,
            "preset": self.preset,
        }


@dataclass(frozen=True)
class Plan:
    frames: int
    fps: int
    resolutions: tuple[Resolution, ...]
    presets: tuple[str, ...]
    targets: tuple[TargetTier, ...]
    jobs: tuple[JobSpec, ...]
    duplicate_targets: int
    max_workers: int

    @property
    def combination_count(self) -> int:
        return len(self.jobs)

    def to_dict(self) -> dict[str, object]:
        return {
            "frames": self.frames,
            "fps": self.fps,
            "resolutions": [r.label for r in self.resolutions],
            "presets": list(self.presets),
            "targets": [
                {"bitrate_kbps": t.bitrate_kbps, "aliases": t.aliases}
                for t in self.targets
            ],
            "jobs": [
                {
                    "resolution": j.resolution.label,
                    "bitrate_kbps": j.bitrate_kbps,
                    "preset": j.preset,
                }
                for j in self.jobs
            ],
            "duplicate_targets": self.duplicate_targets,
            "max_workers": self.max_workers,
        }


def parse_resolution(raw: str) -> Resolution:
    text = raw.strip().lower()
    if "x" not in text:
        raise ConfigError(f"invalid resolution {raw!r}; expected WIDTHxHEIGHT")
    width_text, height_text = text.split("x", 1)
    try:
        width = int(width_text)
        height = int(height_text)
    except ValueError as exc:
        raise ConfigError(f"invalid resolution {raw!r}; expected WIDTHxHEIGHT") from exc
    if width <= 0 or height <= 0:
        raise ConfigError(f"resolution {raw!r} must be positive")
    if width % 2 or height % 2:
        raise ConfigError(f"resolution {raw!r} must use even dimensions for yuv420p")
    if width > MAX_DIMENSION or height > MAX_DIMENSION:
        raise ConfigError(
            f"resolution {raw!r} exceeds the {MAX_DIMENSION}px dimension limit"
        )
    if width * height > MAX_RESOLUTION_PIXELS:
        raise ConfigError(f"resolution {raw!r} exceeds the 1080p pixel budget")
    return Resolution(width, height)


def _split_csv(raw: str, what: str) -> list[str]:
    parts = [part.strip() for part in raw.split(",")]
    parts = [part for part in parts if part]
    if not parts:
        raise ConfigError(f"no {what} supplied")
    return parts


def normalise_resolutions(raw: str) -> tuple[Resolution, ...]:
    seen: dict[Resolution, None] = {}
    for item in _split_csv(raw, "resolutions"):
        seen.setdefault(parse_resolution(item), None)
    return tuple(seen)


def normalise_bitrates(raw: str) -> tuple[tuple[TargetTier, ...], int]:
    requests: list[int] = []
    for item in _split_csv(raw, "bitrates"):
        try:
            value = int(item)
        except ValueError as exc:
            raise ConfigError(f"invalid bitrate {item!r}; expected integer kbps") from exc
        if not (MIN_BITRATE_KBPS <= value <= MAX_BITRATE_KBPS):
            raise ConfigError(
                f"bitrate {value} kbps outside {MIN_BITRATE_KBPS}..{MAX_BITRATE_KBPS}"
            )
        requests.append(value)
    counts: dict[int, int] = {}
    for value in requests:
        counts[value] = counts.get(value, 0) + 1
    targets = tuple(
        TargetTier(bitrate_kbps=value, aliases=count - 1)
        for value, count in sorted(counts.items())
    )
    duplicate_targets = sum(tier.aliases for tier in targets)
    return targets, duplicate_targets


def normalise_presets(raw: str) -> tuple[str, ...]:
    seen: dict[str, None] = {}
    for item in _split_csv(raw, "presets"):
        key = item.lower()
        if key not in ALLOWED_PRESETS:
            raise ConfigError(
                f"unknown preset {item!r}; choose from {', '.join(ALLOWED_PRESETS)}"
            )
        seen.setdefault(key, None)
    return tuple(seen)


def build_plan(
    *,
    frames: int,
    resolutions: str,
    bitrates: str,
    presets: str,
    workers: int = 1,
    fps: int = DEFAULT_FPS,
) -> Plan:
    if not (MIN_FRAMES <= frames <= MAX_FRAMES):
        raise ConfigError(f"frames must be within {MIN_FRAMES}..{MAX_FRAMES}")
    if fps <= 0 or fps > 120:
        raise ConfigError("fps must be within 1..120")
    if not (MIN_WORKERS <= workers <= MAX_WORKERS):
        raise ConfigError(f"workers must be within {MIN_WORKERS}..{MAX_WORKERS}")

    res_list = normalise_resolutions(resolutions)
    preset_list = normalise_presets(presets)
    targets, duplicate_targets = normalise_bitrates(bitrates)

    combination_count = len(res_list) * len(preset_list) * len(targets)
    if combination_count > MAX_SCAN_COMBINATIONS:
        raise ConfigError(
            f"scan expands to {combination_count} jobs; limit is "
            f"{MAX_SCAN_COMBINATIONS}. Narrow resolutions, bitrates or presets."
        )

    jobs = tuple(
        JobSpec(resolution=resolution, bitrate_kbps=target.bitrate_kbps, preset=preset)
        for resolution in res_list
        for preset in preset_list
        for target in targets
    )
    return Plan(
        frames=frames,
        fps=fps,
        resolutions=res_list,
        presets=preset_list,
        targets=targets,
        jobs=jobs,
        duplicate_targets=duplicate_targets,
        max_workers=workers,
    )
