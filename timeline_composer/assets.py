"""Confined, cached PNG loading.

Assets are addressed by POSIX-relative paths inside a single root directory.
Every path is resolved and checked against that root before opening, so a
malicious or mistaken timeline cannot read outside it (including via
symlinks). Decoded images are converted to RGBA and cached by canonical
path; the cache is bounded so large timelines cannot exhaust memory.
"""

from __future__ import annotations

import threading
from collections import OrderedDict
from pathlib import Path
from typing import Iterator

import numpy as np
from PIL import Image, UnidentifiedImageError

DEFAULT_CACHE_SIZE = 32


class AssetError(RuntimeError):
    """Raised when an asset is missing, escapes the root, or is corrupt."""


class AssetStore:
    """Loads PNG assets from ``root`` with an LRU cache of RGBA arrays."""

    def __init__(self, root: str | Path, cache_size: int = DEFAULT_CACHE_SIZE) -> None:
        if cache_size <= 0:
            raise AssetError("cache_size must be positive")
        base = Path(root)
        if not base.is_dir():
            raise AssetError(f"asset root is not a directory: {root}")
        self._root = base.resolve(strict=True)
        self._cache_size = cache_size
        self._cache: OrderedDict[Path, np.ndarray] = OrderedDict()
        self._lock = threading.Lock()

    @property
    def root(self) -> Path:
        return self._root

    def resolve(self, source: str) -> Path:
        """Resolve a relative source path, rejecting anything outside the root."""
        if not isinstance(source, str) or not source:
            raise AssetError("asset source must be a non-empty string")
        if "\x00" in source or "\\" in source:
            raise AssetError(f"invalid asset path: {source!r}")
        candidate = Path(source)
        if candidate.is_absolute():
            raise AssetError(f"asset path must be relative: {source!r}")
        # ``resolve`` collapses '..' segments and follows symlinks; the common
        # path check then guarantees containment even for links out of root.
        resolved = (self._root / candidate).resolve()
        try:
            resolved.relative_to(self._root)
        except ValueError as exc:
            raise AssetError(f"asset escapes root: {source!r}") from exc
        if resolved.suffix.lower() != ".png":
            raise AssetError(f"only PNG assets are supported: {source!r}")
        return resolved

    def load(self, source: str) -> np.ndarray:
        """Return the RGBA ``uint8`` array for ``source`` (cached).

        Safe to call from multiple composition threads: cache access is
        serialised while decoding happens outside the lock.
        """
        path = self.resolve(source)
        with self._lock:
            cached = self._cache.get(path)
            if cached is not None:
                self._cache.move_to_end(path)
                return cached
        frame = self._read(path)
        with self._lock:
            existing = self._cache.get(path)
            if existing is not None:
                self._cache.move_to_end(path)
                return existing
            self._cache[path] = frame
            while len(self._cache) > self._cache_size:
                self._cache.popitem(last=False)
        return frame

    def clear(self) -> None:
        with self._lock:
            self._cache.clear()

    def _read(self, path: Path) -> np.ndarray:
        if not path.is_file():
            raise AssetError(f"asset not found: {path.name}")
        try:
            with Image.open(path) as image:
                image.load()
                rgba = image.convert("RGBA")
        except (UnidentifiedImageError, OSError, ValueError) as exc:
            raise AssetError(f"cannot decode PNG {path.name!r}: {exc}") from exc
        array = np.asarray(rgba, dtype=np.uint8)
        if array.ndim != 3 or array.shape[2] != 4 or array.size == 0:
            raise AssetError(f"decoded asset {path.name!r} is not a valid RGBA image")
        array = np.ascontiguousarray(array)
        array.flags.writeable = False
        return array

    def __len__(self) -> int:
        with self._lock:
            return len(self._cache)

    def __iter__(self) -> Iterator[Path]:
        with self._lock:
            return iter(list(self._cache))


def load_rgba(path: str | Path) -> np.ndarray:
    """Load a single RGBA PNG without confinement (used by fixtures/tests)."""
    try:
        with Image.open(path) as image:
            image.load()
            rgba = image.convert("RGBA")
    except (UnidentifiedImageError, OSError, ValueError) as exc:
        raise AssetError(f"cannot decode PNG {path!r}: {exc}") from exc
    return np.ascontiguousarray(np.asarray(rgba, dtype=np.uint8))


__all__ = ["AssetError", "AssetStore", "DEFAULT_CACHE_SIZE", "load_rgba"]
