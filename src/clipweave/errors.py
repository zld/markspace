"""Typed errors with stable, script-recognizable exit codes.

Exit code contract:
    0  success
    2  input data or exchange-format error
    3  resource limit exceeded
    4  OTIO export failure
"""

EXIT_OK = 0
EXIT_INPUT = 2
EXIT_LIMIT = 3
EXIT_EXPORT = 4


class ClipweaveError(Exception):
    """Base error. ``code`` is a stable machine-readable tag."""

    exit_code = EXIT_INPUT
    code = "E_CLIPWEAVE"

    def __init__(self, message, *, clip_id=None):
        super().__init__(message)
        self.message = message
        self.clip_id = clip_id

    def __str__(self):
        if self.clip_id is not None:
            return f"{self.message} (clip={self.clip_id})"
        return self.message


class ValidationError(ClipweaveError):
    """Timeline model invariant violated."""

    code = "E_VALIDATION"
    exit_code = EXIT_INPUT


class FormatError(ClipweaveError):
    """Exchange document is corrupt or uses an unknown version."""

    code = "E_FORMAT"
    exit_code = EXIT_INPUT


class DowngradeError(FormatError):
    """A v2 timeline cannot be represented losslessly as v1."""

    code = "E_DOWNGRADE"


class LimitError(ClipweaveError):
    """A configured resource limit (e.g. clip count) was exceeded."""

    code = "E_LIMIT"
    exit_code = EXIT_LIMIT


class ExportError(ClipweaveError):
    """OTIO mapping or serialization failed."""

    code = "E_OTIO_EXPORT"
    exit_code = EXIT_EXPORT
