"""Exception hierarchy shared by the cutline toolchain.

Exit-code contract used by the CLI:

* ``0`` - processing completed,
* ``2`` - argument or exchange-format error,
* ``3`` - model validation / resolution / export feasibility error.
"""

from __future__ import annotations


class CutlineError(Exception):
    """Base class for errors that the CLI knows how to report."""

    exit_code = 3

    def __init__(self, message: str, *, track_id: str | None = None,
                 clip_id: str | None = None) -> None:
        self.message = str(message)
        self.track_id = track_id
        self.clip_id = clip_id
        super().__init__(self.render())

    def render(self) -> str:
        locators = []
        if self.track_id is not None:
            locators.append(f"track={self.track_id!r}")
        if self.clip_id is not None:
            locators.append(f"clip={self.clip_id!r}")
        if locators:
            return f"{self.message} ({', '.join(locators)})"
        return self.message


class TimebaseError(CutlineError):
    """A value cannot be represented exactly in the declared timebase."""


class ModelError(CutlineError):
    """The internal timeline model violates an invariant."""


class ExchangeError(CutlineError):
    """An exchange-format document is malformed or unsupported."""

    exit_code = 2


class ConflictError(CutlineError):
    """Overlap scanning could not produce a usable conflict report."""


class ResolutionError(CutlineError):
    """A deterministic resolution policy has no feasible result."""


class BridgeError(CutlineError):
    """The model cannot be expressed as (or read from) OpenTimelineIO."""


class RenderError(CutlineError):
    """Offline frame sampling was requested with invalid parameters."""
