"""Error types shared by the CLI, pipeline, and report layers."""

from __future__ import annotations


class AudioReproError(Exception):
    """Base class for expected, user-facing failures."""


class UsageError(AudioReproError):
    """Invalid command line or parameter combination (exit code 2)."""


class TaskError(AudioReproError):
    """One input file could not be processed (exit code 3)."""

    def __init__(self, path: str, stage: str, reason: str) -> None:
        super().__init__(reason)
        self.path = path
        self.stage = stage
        self.reason = reason

    def as_dict(self) -> dict[str, str]:
        return {"path": self.path, "stage": self.stage, "reason": self.reason}


class OutputError(AudioReproError):
    """Report or output could not be written (exit code 4)."""


class VerifyError(AudioReproError):
    """The report could not be verified (exit code 4)."""
