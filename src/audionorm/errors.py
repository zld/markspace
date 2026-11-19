"""Stable error codes used in per-file records and batch reports."""

E_DECODE = "E_DECODE"            # unreadable / corrupt WAV, or missing sample rate
E_INVALID_DATA = "E_INVALID_DATA"  # NaN/Inf samples
E_LIMIT = "E_LIMIT"              # size / sample-rate / channel-count limits exceeded
E_CONFLICT = "E_CONFLICT"        # two inputs map to the same output path
E_EXISTS = "E_EXISTS"            # target exists and overwrite policy is "fail"
E_WRITE = "E_WRITE"              # write or post-write verification failed
E_CONFIG = "E_CONFIG"            # bad arguments or directory configuration


class AudioNormError(Exception):
    """Processing failure carrying a stable machine-readable code."""

    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code
        self.message = message
