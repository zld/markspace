"""Measurement records and their atomic insertion alongside task completion."""

from __future__ import annotations

import math
import sqlite3
from dataclasses import dataclass

from .db import DONE, RUNNING, StoreBase, StoreError, utc_stamp
from .tasks import Task


@dataclass(frozen=True)
class Measurement:
    task_key: str
    quant_step: int
    block_size: int
    encoded_bytes: int
    actual_kbps: float
    quality_db: float
    payload_sha256: str


class MeasurementOps(StoreBase):
    """Mixin with measurement reads/writes; mixed into :class:`Store`."""

    def complete_task(
        self,
        conn: sqlite3.Connection,
        task: Task,
        measurement: Measurement,
        now: float | None = None,
    ) -> None:
        """Atomically record a measurement and mark the task done."""
        if measurement.task_key != task.task_key:
            raise StoreError("measurement does not belong to the claimed task")
        if (
            not isinstance(measurement.encoded_bytes, int)
            or measurement.encoded_bytes <= 0
        ):
            raise StoreError("refusing to store an invalid byte count")
        if not math.isfinite(measurement.actual_kbps) or measurement.actual_kbps <= 0:
            raise StoreError("refusing to store a non-finite bitrate")
        if not math.isfinite(measurement.quality_db):
            raise StoreError("refusing to store a non-finite quality score")
        stamp = utc_stamp(now)
        with self.transaction(conn):
            cursor = conn.execute(
                "UPDATE tasks SET state = ?, worker = NULL, lease_until = NULL,"
                " last_error = NULL, updated_at = ?"
                " WHERE id = ? AND state = ? AND task_key = ?",
                (DONE, stamp, task.id, RUNNING, task.task_key),
            )
            if cursor.rowcount != 1:
                raise StoreError("cannot complete a task that is not running")
            conn.execute(
                "INSERT INTO measurements (run_id, task_key, quant_step, block_size,"
                " encoded_bytes, actual_kbps, quality_db, payload_sha256, measured_at)"
                " VALUES ((SELECT run_id FROM tasks WHERE id = ?), ?, ?, ?, ?, ?, ?, ?, ?)",
                (
                    task.id,
                    task.task_key,
                    measurement.quant_step,
                    measurement.block_size,
                    measurement.encoded_bytes,
                    measurement.actual_kbps,
                    measurement.quality_db,
                    measurement.payload_sha256,
                    stamp,
                ),
            )

    def measure_task(self, conn: sqlite3.Connection, task_key: str) -> Measurement:
        row = conn.execute(
            "SELECT * FROM measurements WHERE task_key = ?", (task_key,)
        ).fetchone()
        if row is None:
            raise StoreError(f"no measurement for task {task_key}")
        return row_to_measurement(row)

    def measurements(self, run_id: int) -> list[Measurement]:
        conn = self.connect()
        try:
            rows = conn.execute(
                "SELECT * FROM measurements WHERE run_id = ?"
                " ORDER BY actual_kbps, quality_db",
                (run_id,),
            ).fetchall()
            return [row_to_measurement(row) for row in rows]
        finally:
            conn.close()


def row_to_measurement(row: sqlite3.Row) -> Measurement:
    return Measurement(
        task_key=str(row["task_key"]),
        quant_step=int(row["quant_step"]),
        block_size=int(row["block_size"]),
        encoded_bytes=int(row["encoded_bytes"]),
        actual_kbps=float(row["actual_kbps"]),
        quality_db=float(row["quality_db"]),
        payload_sha256=str(row["payload_sha256"]),
    )
