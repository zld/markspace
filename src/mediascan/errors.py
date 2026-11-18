"""Structured error codes shared by every pipeline stage."""

from __future__ import annotations

# Container / IO failures (per-file failure, never silently skipped).
ERR_UNKNOWN_FORMAT = "ERR_UNKNOWN_FORMAT"
ERR_TRUNCATED = "ERR_TRUNCATED"
ERR_BOX_LENGTH = "ERR_BOX_LENGTH"
ERR_BOX_LIMIT = "ERR_BOX_LIMIT"
ERR_DEPTH_LIMIT = "ERR_DEPTH_LIMIT"
ERR_TRACK_LIMIT = "ERR_TRACK_LIMIT"
ERR_FILE_TOO_LARGE = "ERR_FILE_TOO_LARGE"
ERR_FILE_CHANGED = "ERR_FILE_CHANGED"
ERR_PERMISSION = "ERR_PERMISSION"
ERR_IO = "ERR_IO"
ERR_BASELINE_MISSING = "ERR_BASELINE_MISSING"
ERR_INTERNAL = "ERR_INTERNAL"

# Consistency findings (mismatches).
ERR_DURATION_DRIFT = "ERR_DURATION_DRIFT"
ERR_TIMEBASE_DRIFT = "ERR_TIMEBASE_DRIFT"
ERR_CODEC_PARAM = "ERR_CODEC_PARAM"
ERR_PARAM_UNKNOWN = "ERR_PARAM_UNKNOWN"
ERR_TRACK_MISSING = "ERR_TRACK_MISSING"
ERR_TRACK_EXTRA = "ERR_TRACK_EXTRA"
ERR_TRACK_TYPE = "ERR_TRACK_TYPE"

MISMATCH_CATEGORIES = {
    ERR_DURATION_DRIFT: "duration",
    ERR_TIMEBASE_DRIFT: "timebase",
    ERR_CODEC_PARAM: "param",
    ERR_PARAM_UNKNOWN: "param",
    ERR_TRACK_MISSING: "track_missing",
    ERR_TRACK_EXTRA: "track_extra",
    ERR_TRACK_TYPE: "track_type",
}


class ScanFailure(Exception):
    """A per-file failure with a stable code and a byte offset when known."""

    def __init__(
        self,
        code: str,
        detail: str,
        offset: int | None = None,
        track_index: int | None = None,
    ) -> None:
        super().__init__(detail)
        self.code = code
        self.detail = detail
        self.offset = offset
        self.track_index = track_index

    def as_dict(self) -> dict:
        return {
            "error_code": self.code,
            "error_detail": self.detail,
            "error_offset": self.offset,
        }
