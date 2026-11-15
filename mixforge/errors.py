"""Exception types and process exit codes.

Exit code contract:
    0  success
    2  argument or manifest validation error
    3  media I/O or processing failure
    4  strict clip threshold exceeded
"""

EXIT_OK = 0
EXIT_USAGE = 2
EXIT_MEDIA = 3
EXIT_CLIP = 4


class MixForgeError(Exception):
    """Base error carrying the process exit code."""

    exit_code = EXIT_MEDIA


class ManifestError(MixForgeError):
    """Invalid project manifest or command arguments."""

    exit_code = EXIT_USAGE


class MediaError(MixForgeError):
    """Audio decode/encode, non-finite data, or output write failure."""

    exit_code = EXIT_MEDIA
