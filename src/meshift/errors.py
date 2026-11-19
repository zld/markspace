"""Exception hierarchy for the meshift pipeline.

Exit-code contract used by the CLI:

* :data:`EXIT_CORRUPT` (2) - malformed/truncated input or failed index check
* :data:`EXIT_USAGE`   (3) - bad arguments, refused overwrite, size limit
"""

EXIT_OK = 0
EXIT_CORRUPT = 2
EXIT_USAGE = 3


class MeshiftError(Exception):
    """Base class for every meshift failure."""

    exit_code = EXIT_CORRUPT


class EBMLError(MeshiftError):
    """Malformed EBML structure (bad VINT, truncation, out-of-bounds)."""

    def __init__(self, message: str, offset: int | None = None,
                 path: tuple[str, ...] = ()):
        where = f" at offset {offset}" if offset is not None else ""
        trail = f" [path: {'/'.join(path)}]" if path else ""
        super().__init__(f"{message}{where}{trail}")
        self.offset = offset
        self.path = path


class MatroskaError(MeshiftError):
    """Matroska semantic problem that cannot be explained losslessly."""


class TruncatedInputError(MatroskaError):
    """Input ends before an element or block declares it should."""


class UnsupportedLacingError(MatroskaError):
    """A block uses lacing, which meshift does not split losslessly."""


class UnsupportedCodecError(MatroskaError):
    """Codec identifier is not in the remux whitelist."""


class TimelineError(MatroskaError):
    """Timestamps cannot be normalized without dropping information."""


class MP4WriteError(MeshiftError):
    """Failure while serialising the MP4 container."""


class IndexCheckError(MeshiftError):
    """Written MP4 index does not match the sample table it was built from."""


class SizeLimitError(MeshiftError):
    """Output would exceed the single-file size ceiling."""

    exit_code = EXIT_USAGE


class UsageError(MeshiftError):
    """Command-line usage problem or refused overwrite."""

    exit_code = EXIT_USAGE
