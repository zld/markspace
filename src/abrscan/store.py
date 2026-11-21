"""Public store facade.

The implementation is split across :mod:`abrscan.db` (schema, runs),
:mod:`abrscan.tasks` (scheduling state) and :mod:`abrscan.records`
(measurements).  This module re-exports the names callers and tests use.
"""

from __future__ import annotations

from .db import (
    DEFAULT_LEASE_SECONDS,
    DONE,
    FAILED,
    MAX_ATTEMPTS,
    PENDING,
    RUNNING,
    SCHEMA_VERSION,
    STATES,
    RunInfo,
    StoreBase,
    StoreError,
    check_db_path,
    utc_stamp,
)
from .records import Measurement, MeasurementOps
from .tasks import Task, TaskOps


class Store(TaskOps, MeasurementOps):
    """Complete store: runs, task lifecycle and measurement records."""


__all__ = [
    "DEFAULT_LEASE_SECONDS",
    "DONE",
    "FAILED",
    "MAX_ATTEMPTS",
    "PENDING",
    "RUNNING",
    "SCHEMA_VERSION",
    "STATES",
    "Measurement",
    "RunInfo",
    "Store",
    "StoreBase",
    "StoreError",
    "Task",
    "check_db_path",
    "utc_stamp",
]
