"""Typed errors and process exit codes for mixforge.

Exit code contract:
    0  completed, ``error`` clip policy not triggered
    2  invalid manifest or command-line parameters
    3  unreadable / unwritable audio asset or report target
    4  ``error`` clip policy enabled and clipping was detected
"""

from __future__ import annotations

EXIT_OK = 0
EXIT_INVALID = 2
EXIT_IO = 3
EXIT_CLIP = 4


class MixforgeError(Exception):
    """Base class for all mixforge failures carrying an exit code."""

    exit_code = EXIT_INVALID


class ManifestError(MixforgeError):
    """The project manifest is structurally or semantically invalid."""

    exit_code = EXIT_INVALID

    def __init__(self, message: str, field: str | None = None) -> None:
        if field:
            message = f"{field}: {message}"
        super().__init__(message)
        self.field = field


class AssetError(MixforgeError):
    """An audio file cannot be read or written."""

    exit_code = EXIT_IO


class LimitError(ManifestError):
    """A manifest value exceeds a hard safety limit."""

    exit_code = EXIT_INVALID


class ClipPolicyError(MixforgeError):
    """The ``error`` clip policy was triggered by an over-level sample."""

    exit_code = EXIT_CLIP
