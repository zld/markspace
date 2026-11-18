"""Exception hierarchy for the media_index package.

All parse failures carry an absolute byte offset (or -1 when the failure
is not tied to a concrete position) and a short machine-readable reason.
The CLI maps :class:`MediaIndexError` subclasses to stable exit codes and
never lets a raw traceback reach the user.
"""

from __future__ import annotations


class MediaIndexError(Exception):
    """Base class for every expected failure raised by this package."""

    exit_code = 2

    def __init__(self, reason: str, offset: int = -1) -> None:
        self.reason = reason
        self.offset = offset
        if offset >= 0:
            super().__init__(f"{reason} (at byte {offset})")
        else:
            super().__init__(reason)


class ParseError(MediaIndexError):
    """Malformed structure: truncated header, bad length, bad field value."""

    exit_code = 2


class LimitError(MediaIndexError):
    """Input exceeds one of the hard limits in :mod:`media_index.limits`."""

    exit_code = 2


class ValidationFailure(MediaIndexError):
    """File parsed, but cross-checks found inconsistent data."""

    exit_code = 1


class NestedIndexError(ParseError):
    """An index reference cannot be resolved (depth/cycle/missing target)."""
