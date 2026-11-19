"""Typed exceptions with CLI exit-code and location semantics."""

from __future__ import annotations

from typing import Any


class MixForgeError(Exception):
    """Base error.  ``exit_code`` is consumed by the CLI."""

    exit_code = 1
    kind = "internal"

    def __init__(
        self,
        message: str,
        *,
        location: str | None = None,
        context: dict[str, Any] | None = None,
    ) -> None:
        super().__init__(message)
        self.message = message
        self.location = location
        self.context = dict(context or {})

    def __str__(self) -> str:
        if self.location:
            return f"{self.location}: {self.message}"
        return self.message

    def as_dict(self) -> dict[str, Any]:
        data: dict[str, Any] = {
            "status": "error",
            "error": self.kind,
            "message": self.message,
            "exit_code": self.exit_code,
        }
        if self.location is not None:
            data["location"] = self.location
        if self.context:
            data["context"] = self.context
        return data


class ConfigError(MixForgeError):
    """Invalid project JSON, CLI argument, or clip duration."""

    exit_code = 2
    kind = "config"


class AudioIOError(MixForgeError):
    """WAV/JSON read-write failure or invalid audio payload."""

    exit_code = 2
    kind = "io"


class ProcessingError(MixForgeError):
    """Internal render failure, including non-finite samples."""

    exit_code = 1
    kind = "processing"


class MismatchError(MixForgeError):
    """Offline and streaming sample streams differ."""

    exit_code = 1
    kind = "mismatch"
