"""Exception hierarchy with the CLI exit codes from the specification.

Exit codes
----------
0  success
2  parameter or manifest validation error (:class:`ManifestError`)
3  audio read / resample error (:class:`AudioError`)
4  output write failure (:class:`WriteError`)
5  offline/streaming verification failure (:class:`StreamVerifyError`)

Unexpected errors keep the conventional exit code 1.
"""


class TrackMixError(Exception):
    """Base class for all expected, user-facing trackmix errors."""

    exit_code = 1


class ManifestError(TrackMixError):
    """Invalid command line parameters or an invalid manifest."""

    exit_code = 2


class AudioError(TrackMixError):
    """A WAV file could not be read/decoded or resampling failed."""

    exit_code = 3


class WriteError(TrackMixError):
    """An output file (WAV/JSON) could not be written."""

    exit_code = 4


class StreamVerifyError(TrackMixError):
    """Streaming output diverged from the offline reference."""

    exit_code = 5
