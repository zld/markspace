"""Exception types used to map failures onto CLI exit codes."""

from __future__ import annotations


class ConfigError(ValueError):
    """Invalid user-supplied configuration; the CLI exits with status 2."""


class SimulationError(RuntimeError):
    """A trace or simulation invariant failed at runtime; the CLI exits with status 1."""
