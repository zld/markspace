"""Typed errors carrying a machine-readable location and exit code."""

from __future__ import annotations


class MixforgeError(Exception):
    """Base error. ``exit_code`` decides the CLI status."""

    exit_code = 3

    def __init__(self, message: str, *, track_id: str | None = None,
                 field: str | None = None) -> None:
        super().__init__(message)
        self.message = message
        self.track_id = track_id
        self.field = field

    def __str__(self) -> str:
        parts = [p for p in (self.track_id, self.field) if p]
        if not parts:
            return self.message
        return f"[{' '.join(parts)}] {self.message}"

    def as_dict(self) -> dict:
        return {
            "type": type(self).__name__,
            "message": self.message,
            "track_id": self.track_id,
            "field": self.field,
            "exit_code": self.exit_code,
        }


class ManifestError(MixforgeError):
    """Manifest or CLI argument is invalid."""

    exit_code = 2


class RenderError(MixforgeError):
    """Source media, resampling or output writing failed."""

    exit_code = 3


class ConsistencyError(MixforgeError):
    """Offline and streaming renders disagree."""

    exit_code = 4
