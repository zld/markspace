"""Error types shared by every audiomix module.

Exit-code contract used by the CLI:

* ``0``  success
* ``2``  input or processing error (bad manifest, audio, automation, ...)
* ``3``  internal error (unexpected exception)
"""

from __future__ import annotations

EXIT_OK = 0
EXIT_INPUT = 2
EXIT_INTERNAL = 3


class AudioMixError(Exception):
    """Base class for all user-facing audiomix failures (exit code 2)."""

    exit_code = EXIT_INPUT

    def __init__(self, message: str) -> None:
        super().__init__(message)
        self.message = message


class ManifestError(AudioMixError):
    """The session manifest is missing, malformed, or semantically invalid."""


class TrackError(AudioMixError):
    """A track failed to load, map to channels, or render.

    Always carries the offending track name so the terminal report can point
    at the exact source of the problem.
    """

    def __init__(self, track: str, reason: str) -> None:
        super().__init__(f"track '{track}': {reason}")
        self.track = track
        self.reason = reason


class AutomationError(AudioMixError):
    """An automation curve contains invalid points or values."""


class ResampleError(AudioMixError):
    """Sampling-rate conversion could not be performed safely."""


class OutputError(AudioMixError):
    """Writing a WAV or report artifact failed."""
