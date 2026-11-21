"""Typed errors that always carry the offending path plus a reason.

Every error surfaced to the CLI is an :class:`AudioSliceError` subclass so
that the user gets a locatable message (file path + cause) and the CLI can
map the error family to a stable exit code.
"""

from __future__ import annotations


class AudioSliceError(Exception):
    """Base error; ``path`` is the file or database the error refers to."""

    def __init__(self, path: str, reason: str) -> None:
        self.path = path
        self.reason = reason
        super().__init__(f"{path}: {reason}")


class InputFileError(AudioSliceError):
    """The input path is missing, unreadable or otherwise unusable."""


class WavFormatError(AudioSliceError):
    """The WAV container or sample format is rejected by the reader."""


class StoreError(AudioSliceError):
    """A SQLite read/write failure (lock, corruption, I/O error)."""


class ConfigError(AudioSliceError):
    """Invalid detection parameters supplied by the caller."""

    def __init__(self, reason: str) -> None:
        super().__init__(path="<parameters>", reason=reason)
