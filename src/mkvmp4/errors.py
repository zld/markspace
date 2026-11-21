"""Typed failures with stable CLI exit codes and locatable context.

Every error raised by this package carries the structural context needed to
find the offending bytes: absolute input offset, element path in the EBML
tree, track number and sample index where applicable.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

EXIT_OK = 0
EXIT_USAGE = 2
EXIT_INPUT = 3
EXIT_TIMESTAMP = 4
EXIT_OUTPUT = 5


@dataclass
class ErrorContext:
    """Where a failure happened, in terms the caller can verify by hand."""

    stage: str
    offset: int | None = None
    path: str | None = None
    track: int | None = None
    sample_index: int | None = None
    element_name: str | None = None
    detail: str | None = None

    def to_dict(self) -> dict[str, Any]:
        out: dict[str, Any] = {"stage": self.stage}
        for key in ("offset", "path", "track", "sample_index", "element_name", "detail"):
            value = getattr(self, key)
            if value is not None:
                out[key] = value
        return out


class MkvMp4Error(Exception):
    """Base error: exit code, machine-readable context and a message."""

    exit_code: int = EXIT_INPUT
    stage: str = "parse"

    def __init__(
        self,
        message: str,
        *,
        offset: int | None = None,
        path: str | None = None,
        track: int | None = None,
        sample_index: int | None = None,
        element_name: str | None = None,
        stage: str | None = None,
    ) -> None:
        super().__init__(message)
        self.message = message
        self.context = ErrorContext(
            stage or self.stage,
            offset=offset,
            path=path,
            track=track,
            sample_index=sample_index,
            element_name=element_name,
        )

    def to_dict(self) -> dict[str, Any]:
        return {
            "error": type(self).__name__,
            "message": self.message,
            "context": self.context.to_dict(),
            "exit_code": self.exit_code,
        }


class UsageError(MkvMp4Error):
    exit_code = EXIT_USAGE
    stage = "arguments"


class InputError(MkvMp4Error):
    exit_code = EXIT_INPUT


class EbmlParseError(InputError):
    stage = "ebml"


class BoundaryError(InputError):
    stage = "boundary"


class UnsupportedError(InputError):
    stage = "unsupported"


class TrackMappingError(InputError):
    stage = "mapping"


class TimestampPolicyError(MkvMp4Error):
    exit_code = EXIT_TIMESTAMP
    stage = "timestamps"


class OutputError(MkvMp4Error):
    exit_code = EXIT_OUTPUT
    stage = "output"


class VerifyError(OutputError):
    stage = "verify"


