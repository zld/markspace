"""Typed errors carrying the process exit code.

Exit code contract:
    0  success
    1  media processing failure (decode / codec / reorder overflow)
    2  parameter error (bad CLI arguments, stream index out of range)
    3  output error (path collision, unwritable destination)
"""

from __future__ import annotations


class FrameAuditError(Exception):
    """Base class for all frameaudit errors."""

    exit_code = 1


class MediaError(FrameAuditError):
    """The media itself could not be processed."""

    exit_code = 1


class NoVideoStreamError(MediaError):
    """No video stream exists in the container."""


class CodecNotSupportedError(MediaError):
    """The stream's codec cannot be decoded by the local FFmpeg build."""


class ReorderLimitError(MediaError):
    """The bounded reorder buffer would have to grow past its limit."""


class UsageError(FrameAuditError):
    """Invalid command-line parameters."""

    exit_code = 2


class OutputError(FrameAuditError):
    """An output path cannot be written or already exists."""

    exit_code = 3
