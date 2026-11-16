"""Exception hierarchy for the whole pipeline.

Exit code mapping (used by the CLI):
    2 -> UsageError
    3 -> CorruptMKVError / UnsupportedCodecError
    4 -> IOFailure
    5 -> IndexValidationError
"""


class Mkvmp4Error(Exception):
    """Base class for every error raised by the package."""

    exit_code = 1


class UsageError(Mkvmp4Error):
    """Invalid command line arguments."""

    exit_code = 2


class CorruptMKVError(Mkvmp4Error):
    """Truncated or structurally invalid Matroska/EBML input.

    ``offset`` is the byte position where the problem was detected and
    ``path`` is the EBML element path (list of element names) active then.
    """

    exit_code = 3

    def __init__(self, message: str, offset: int | None = None,
                 path: list[str] | None = None):
        self.offset = offset
        self.path = list(path) if path else []
        location = ""
        if offset is not None:
            location = f" at byte {offset}"
        if self.path:
            location += f" in {'/'.join(self.path)}"
        super().__init__(f"{message}{location}")


class UnsupportedCodecError(Mkvmp4Error):
    """A track uses a codec that the remuxer does not repackage."""

    exit_code = 3

    def __init__(self, message: str, track_numbers: list[int] | None = None):
        self.track_numbers = sorted(track_numbers or [])
        if self.track_numbers:
            nums = ", ".join(str(n) for n in self.track_numbers)
            message = f"{message} (track number(s): {nums})"
        super().__init__(message)


class IOFailure(Mkvmp4Error):
    """File system / read / write failure."""

    exit_code = 4


class LimitExceededError(CorruptMKVError):
    """Nesting depth, sample count or packet size limit exceeded."""

    exit_code = 3


class IndexValidationError(Mkvmp4Error):
    """Pre-write / post-write MP4 sample table consistency check failed."""

    exit_code = 5
