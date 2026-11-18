"""Small filesystem helpers for deterministic fixture materialization.

Kept separate from the pure builders in ``tests.fixtures`` so the library
package does not need to import the test tree at module load time.
"""

from __future__ import annotations

import os


def ensure_dir(path: str) -> None:
    os.makedirs(path, exist_ok=True)


def write_if_missing(path: str, blob: bytes) -> bool:
    """Write ``blob`` only when ``path`` does not exist yet.

    Returns True when the file was created, False when it already existed.
    The write is atomic via a temporary sibling file plus ``os.replace``.
    """

    if os.path.exists(path):
        return False
    tmp = f"{path}.tmp"
    with open(tmp, "wb") as handle:
        handle.write(blob)
    os.replace(tmp, path)
    return True
