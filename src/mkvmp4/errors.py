"""Typed errors carrying an error category and the CLI exit code they imply.

Exit code contract (see cli.py):
    2 - user/input/format errors (bad arguments, malformed or unsupported input)
    3 - conversion or verification failures
    1 - unexpected internal failure
"""

from __future__ import annotations


class MkvMp4Error(Exception):
    """Base class for all expected, explainable failures."""

    exit_code = 3
    category = "error"

    def __init__(self, message: str, *, offset: int | None = None,
                 element_id: int | None = None) -> None:
        super().__init__(message)
        self.message = message
        self.offset = offset
        self.element_id = element_id

    def describe(self) -> str:
        loc = []
        if self.offset is not None:
            loc.append(f"offset=0x{self.offset:X}")
        if self.element_id is not None:
            loc.append(f"element_id=0x{self.element_id:X}")
        suffix = f" ({', '.join(loc)})" if loc else ""
        return f"{self.category}: {self.message}{suffix}"


class InputFormatError(MkvMp4Error):
    """The input file or CLI arguments are invalid or unsupported."""

    exit_code = 2
    category = "input_format"


class EbmlError(InputFormatError):
    """Malformed EBML structure: truncated VINT, crossed boundary, bad ID."""

    category = "ebml"


class LimitExceededError(InputFormatError):
    """A configured safety limit (bytes, elements, sample size) was exceeded."""

    category = "limit_exceeded"


class CodecRejectedError(InputFormatError):
    """A codec/parameter combination cannot be safely mapped into MP4."""

    category = "codec_rejected"


class ConversionError(MkvMp4Error):
    """The remux itself failed despite valid, supported input."""

    category = "conversion"


class VerificationError(MkvMp4Error):
    """A written MP4 failed the read-back verification pass."""

    category = "verification"
