"""Error types with stable CLI exit-code mapping."""

from __future__ import annotations

EXIT_OK = 0
EXIT_FILE_FAILED = 1
EXIT_USAGE = 2

#: Highest documented sample rate we accept as input or target.
MAX_SAMPLE_RATE = 384_000
#: Highest channel count we accept as input; the pipeline never widens.
MAX_CHANNELS = 32
#: Hard cap on frames per input file (1 hour at 192 kHz).
MAX_FRAMES = 192_000 * 3600
#: Hard cap on input file size in bytes (1 GiB).
MAX_FILE_BYTES = 1 * 1024 * 1024 * 1024


class AudioPipeError(Exception):
    """Base class for errors that carry a machine-readable reason."""

    reason = "error"
    exit_code = EXIT_FILE_FAILED

    def __init__(self, message: str, *, reason: str | None = None) -> None:
        super().__init__(message)
        self.message = message
        if reason is not None:
            self.reason = reason

    def as_dict(self) -> dict[str, str]:
        return {"reason": self.reason, "message": self.message}


class UsageError(AudioPipeError):
    """Bad command-line arguments or invalid configuration."""

    reason = "usage"
    exit_code = EXIT_USAGE


class InputError(AudioPipeError):
    """The input file exists but is unusable (format, limits, layout)."""

    reason = "input"


class ProcessingError(AudioPipeError):
    """Processing a valid file failed (write error, DSP invariant, ...)."""

    reason = "processing"
