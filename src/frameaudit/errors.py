"""Exception hierarchy for frameaudit.

The CLI maps these exceptions to documented process exit codes:

* :class:`InputError` / :class:`UsageError` -> exit code 2
* :class:`DecodeError` / :class:`OutputWriteError` /
  :class:`LimitExceededError` -> exit code 1
"""

from __future__ import annotations


class FrameAuditError(Exception):
    """Base class for all frameaudit errors with a stable exit code."""

    exit_code: int = 1


class UsageError(FrameAuditError):
    """Invalid command-line arguments or option combinations."""

    exit_code = 2


class InputError(FrameAuditError):
    """The input file cannot be opened, has no video stream or no frames."""

    exit_code = 2


class DecodeError(FrameAuditError):
    """A failure occurred while demuxing or decoding video."""

    exit_code = 1


class OutputWriteError(FrameAuditError):
    """Writing the JSON report failed; the failure must not be swallowed."""

    exit_code = 1


class LimitExceededError(FrameAuditError):
    """A hard bound on frame records or reorder buffer size was exceeded."""

    exit_code = 1
