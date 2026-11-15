"""audiomix: deterministic offline/streaming multi-track audio mixer."""

__version__ = "0.1.0"

#: Hard limits shared by every module.
MIN_SAMPLE_RATE = 8000
MAX_SAMPLE_RATE = 192000
MAX_TRACKS = 32
MIN_BLOCK_FRAMES = 1
MAX_BLOCK_FRAMES = 8192

#: Process exit codes used by the CLI.
EXIT_OK = 0
EXIT_CONFIG = 2
EXIT_PROCESS = 3
EXIT_WRITE = 4


class AudioMixError(Exception):
    """Base class for all audiomix failures."""


class ConfigError(AudioMixError):
    """Invalid project/track/automation configuration (exit code 2)."""


class AudioProcessError(AudioMixError):
    """Decoding, resampling or graph processing failure (exit code 3)."""


class WriteError(AudioMixError):
    """Output file write failure (exit code 4)."""
