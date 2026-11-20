"""Manifest loading and per-asset track comparison."""
from __future__ import annotations

import json
from pathlib import Path

from .model import Inconsistency, ManifestError, MediaInfo, TrackInfo

CODEC_ALIASES = {"avc1": "avc1", "h264": "avc1", "aac": "aac", "mp4a": "aac", "pcm": "pcm"}


def load_manifest(path: Path) -> dict:
    try:
        doc = json.loads(path.read_text(encoding="utf-8"))
    except OSError as e:
        raise ManifestError(f"cannot read manifest {path}: {e}")
    except json.JSONDecodeError as e:
        raise ManifestError(f"manifest {path} is not valid JSON: {e}")
    if not isinstance(doc, dict) or not isinstance(doc.get("assets"), list) or not doc["assets"]:
        raise ManifestError(f"manifest {path}: expected an object with a non-empty 'assets' list")
    for i, asset in enumerate(doc["assets"]):
        if not isinstance(asset, dict) or not isinstance(asset.get("path"), str):
            raise ManifestError(f"manifest {path}: asset[{i}] needs a string 'path'")
        tracks = asset.get("tracks")
        if not isinstance(tracks, list) or not tracks:
            raise ManifestError(f"manifest {path}: asset[{i}] needs a non-empty 'tracks' list")
        for j, t in enumerate(tracks):
            where = f"asset[{i}].tracks[{j}]"
            if not isinstance(t, dict) or t.get("type") not in ("video", "audio"):
                raise ManifestError(f"manifest {path}: {where} needs type 'video' or 'audio'")
            if CODEC_ALIASES.get(str(t.get("codec", "")).lower()) is None:
                raise ManifestError(f"manifest {path}: {where} has unknown codec {t.get('codec')!r}")
            if not isinstance(t.get("duration_ms"), int) or t["duration_ms"] < 0:
                raise ManifestError(f"manifest {path}: {where} needs a non-negative integer 'duration_ms'")
            if t["type"] == "audio" and "language" in t and not isinstance(t["language"], str):
                raise ManifestError(f"manifest {path}: {where} language must be a string")
    return doc


def _expected_key(t: dict) -> tuple[str, str | None]:
    return (t["type"], t.get("language") if t["type"] == "audio" else None)


def _actual_key(t: TrackInfo) -> tuple[str, str | None]:
    return (t.kind, t.language if t.kind == "audio" else None)


def _label(key: tuple[str, str | None]) -> str:
    return f"{key[0]}:{key[1]}" if key[1] else key[0]


def _check_duplicates(rel: str, keys: list[tuple[str, str | None]], side: str,
                      issues: list[Inconsistency]) -> None:
    seen: set[tuple[str, str | None]] = set()
    for key in keys:
        if key[1] is None:
            continue
        if key in seen:
            issues.append(Inconsistency("duplicate-language", rel,
                                        f"{rel}: {side} lists language {key[1]!r} more than once",
                                        track=_label(key)))
        seen.add(key)


def compare_asset(rel: str, media: MediaInfo | None, extract_problems: list[Inconsistency],
                  expected: dict, tolerance_ms: int) -> tuple[list[Inconsistency], int]:
    """Match actual tracks against the manifest entry.

    Returns (inconsistencies, max_duration_delta_ms). Extraction problems
    are carried through so a failed file still reports its own errors.
    """
    issues = list(extract_problems)
    if media is None:
        return issues, 0
    expected_tracks: list[dict] = expected["tracks"]
    actual = media.tracks
    _check_duplicates(rel, [_expected_key(t) for t in expected_tracks], "manifest", issues)
    _check_duplicates(rel, [_actual_key(t) for t in actual], "media", issues)

    used: set[int] = set()
    max_delta = 0
    for et in expected_tracks:
        key = _expected_key(et)
        label = _label(key)
        matches = [i for i, a in enumerate(actual) if i not in used and _actual_key(a) == key]
        if not matches:
            issues.append(Inconsistency("missing-track", rel,
                                        f"{rel}: manifest track {label} not found in media",
                                        track=label))
            continue
        a = actual[matches[0]]
        used.add(matches[0])
        want_codec = CODEC_ALIASES[str(et["codec"]).lower()]
        if a.codec.codec != want_codec:
            issues.append(Inconsistency("codec-mismatch", rel,
                                        f"{rel}: {label} codec {a.codec.codec!r} != manifest {want_codec!r}",
                                        track=label))
            continue
        diffs = {}
        for field_name in ("width", "height", "sample_rate", "channels"):
            want = et.get(field_name)
            got = getattr(a.codec, field_name)
            if want is not None and want != got:
                diffs[field_name] = {"expected": want, "actual": got}
        if diffs:
            summary = ", ".join(f"{k} {v['actual']} != {v['expected']}" for k, v in diffs.items())
            issues.append(Inconsistency("codec-param-mismatch", rel,
                                        f"{rel}: {label} codec parameters differ: {summary}",
                                        track=label, detail=diffs))
        delta = abs(a.duration_ms - et["duration_ms"])
        max_delta = max(max_delta, delta)
        if delta > tolerance_ms:
            issues.append(Inconsistency(
                "duration-delta", rel,
                f"{rel}: {label} duration {a.duration_ms}ms differs from manifest {et['duration_ms']}ms "
                f"by {delta}ms (tolerance {tolerance_ms}ms)",
                track=label,
                detail={"expected_ms": et["duration_ms"], "actual_ms": a.duration_ms, "delta_ms": delta}))
    for i, a in enumerate(actual):
        if i not in used:
            issues.append(Inconsistency("extra-track", rel,
                                        f"{rel}: media has unlisted {_label(_actual_key(a))} track "
                                        f"(id {a.track_id}, {a.codec.summary()})",
                                        track=a.locator or None))
    return issues, max_delta
