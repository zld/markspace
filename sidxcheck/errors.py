"""Typed failures shared by the parsing, planning and writing layers.

Exit code contract for the CLI:
    0  success
    2  the media structure or an index failed validation
    3  a read/write or rewrite operation failed
"""

from __future__ import annotations


class SidxCheckError(Exception):
    """Base class for all deliberate failures raised by this package."""

    exit_code = 2

    def __init__(self, message: str, *, location: str | None = None) -> None:
        super().__init__(message)
        self.message = message
        self.location = location

    def __str__(self) -> str:  # pragma: no cover - trivial
        if self.location:
            return f"{self.location}: {self.message}"
        return self.message


class MediaStructureError(SidxCheckError):
    """The file cannot be trusted as a classic, non-fragmented MP4."""

    exit_code = 2


class IndexValidationError(SidxCheckError):
    """SIDX or sample-table values disagree with the sample data."""

    exit_code = 2


class UnsupportedFeatureError(MediaStructureError):
    """A structurally valid but explicitly out-of-scope feature was found."""


class IOErrorFailure(SidxCheckError):
    """Reading, writing, or replacing files failed."""

    exit_code = 3


class BoxLimitError(MediaStructureError):
    """A box or sample count exceeded a configured safety limit."""
