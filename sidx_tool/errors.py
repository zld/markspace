"""Typed errors and their process exit codes.

Exit code contract:
    0  success
    2  corrupt / unsupported container format
    3  rebuilt output fails cross-validation
    4  file read / write failure
"""

from __future__ import annotations


class SidxToolError(Exception):
    """Base class for every error raised by this package."""

    exit_code = 1


class FormatError(SidxToolError):
    """Container is malformed, truncated or uses an unsupported feature."""

    exit_code = 2


class LimitError(FormatError):
    """A hard safety limit (depth, count, size) would be exceeded."""

    exit_code = 2


class ValidationError(SidxToolError):
    """Cross-validation of the rebuilt file found an inconsistency."""

    exit_code = 3


class FileError(SidxToolError):
    """Local file system read/write failure."""

    exit_code = 4
