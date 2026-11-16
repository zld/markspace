"""Expand and normalize the encode parameter grid.

Resolutions, target bitrates and presets are parsed from CLI strings,
validated against the 1080p cap, and expanded into a deduplicated task
list. Duplicate target bitrates collapse into a single tier whose
``alias_count`` records how many aliases were merged.
"""

from __future__ import annotations

from dataclasses import dataclass

from .fixtures import MAX_HEIGHT, MAX_WIDTH

X264_PRESETS = (
    "ultrafast",
    "superfast",
    "veryfast",
    "faster",
    "fast",
    "medium",
    "slow",
    "slower",
    "veryslow",
)

MIN_TARGET_KBPS = 10
MAX_TARGET_KBPS = 50_000


@dataclass(frozen=True)
class Resolution:
    width: int
    height: int


@dataclass(frozen=True)
class PlannedTask:
    width: int
    height: int
    target_kbps: int
    preset: str
    alias_count: int

    @property
    def params_key(self) -> str:
        """Canonical identity of an encode; identical keys encode once."""
        return f"{self.width}x{self.height}:{self.target_kbps}k:{self.preset}"


def parse_resolutions(text: str) -> list[Resolution]:
    out: list[Resolution] = []
    for item in text.split(","):
        item = item.strip().lower()
        if not item:
            continue
        try:
            w_s, h_s = item.split("x", 1)
            w, h = int(w_s), int(h_s)
        except ValueError:
            raise ValueError(f"bad resolution {item!r}; expected WxH") from None
        if w % 2 or h % 2:
            raise ValueError(f"resolution {item!r} must have even dimensions")
        if w < 16 or h < 16 or w > MAX_WIDTH or h > MAX_HEIGHT:
            raise ValueError(f"resolution {item!r} outside 16x16..{MAX_WIDTH}x{MAX_HEIGHT}")
        res = Resolution(w, h)
        if res not in out:
            out.append(res)
    if not out:
        raise ValueError("no resolutions given")
    return out


def parse_presets(text: str) -> list[str]:
    out: list[str] = []
    for item in text.split(","):
        item = item.strip().lower()
        if not item:
            continue
        if item not in X264_PRESETS:
            raise ValueError(f"unknown x264 preset {item!r}")
        if item not in out:
            out.append(item)
    if not out:
        raise ValueError("no presets given")
    return out


def parse_targets(text: str) -> list[int]:
    out: list[int] = []
    for item in text.split(","):
        item = item.strip()
        if not item:
            continue
        try:
            kbps = int(item)
        except ValueError:
            raise ValueError(f"bad target bitrate {item!r}; expected integer kbps") from None
        if not (MIN_TARGET_KBPS <= kbps <= MAX_TARGET_KBPS):
            raise ValueError(
                f"target {kbps} kbps outside {MIN_TARGET_KBPS}..{MAX_TARGET_KBPS}"
            )
        out.append(kbps)
    if not out:
        raise ValueError("no target bitrates given")
    return out


def normalize_targets(targets: list[int]) -> dict[int, int]:
    """Collapse duplicate targets into one tier; value -> alias_count."""
    tiers: dict[int, int] = {}
    for kbps in targets:
        tiers[kbps] = tiers.get(kbps, 0) + 1
    return dict(sorted(tiers.items()))


def expand_grid(
    resolutions_text: str, targets_text: str, presets_text: str
) -> list[PlannedTask]:
    """Parse CLI strings and expand the full deduplicated task grid."""
    resolutions = parse_resolutions(resolutions_text)
    presets = parse_presets(presets_text)
    tiers = normalize_targets(parse_targets(targets_text))
    tasks: list[PlannedTask] = []
    seen: set[str] = set()
    for target, alias_count in tiers.items():
        for res in resolutions:
            for preset in presets:
                task = PlannedTask(res.width, res.height, target, preset, alias_count)
                if task.params_key in seen:
                    continue
                seen.add(task.params_key)
                tasks.append(task)
    return tasks
