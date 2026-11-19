"""Typed errors and the exit-code contract they map to.

Exit codes are part of the public interface:

====  ==========================================================
0     the scan completed for every requested input
1     an input could not be read, decoded or was unusable
2     the command line was invalid
3     the requested output file could not be written
====  ==========================================================
"""

from __future__ import annotations

EXIT_OK = 0
EXIT_INPUT = 1
EXIT_USAGE = 2
EXIT_OUTPUT = 3


class AuditError(Exception):
    """An error that maps onto a documented process exit code."""

    exit_code = EXIT_INPUT

    def __init__(self, message: str, *, reason: str = "error") -> None:
        super().__init__(message)
        self.message = message
        self.reason = reason


class InputError(AuditError):
    """The input could not be opened or does not describe usable video."""

    exit_code = EXIT_INPUT


class DecodeError(AuditError):
    """The decoder failed part-way through the stream."""

    exit_code = EXIT_INPUT


class OutputError(AuditError):
    """The output sink could not be written."""

    exit_code = EXIT_OUTPUT
