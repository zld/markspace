"""Error hierarchy shared by the parser, layout and command-line layers.

Every structural or consistency problem carries the box type and absolute byte
offset that triggered it, so diagnostics can point at the damaged container
instead of merely saying that the file did not parse.
"""

from __future__ import annotations


class MediidxError(Exception):
    """Base class for errors that map onto a process exit code."""

    exit_code = 2

    def __init__(
        self,
        message: str,
        *,
        box_type: bytes | str | None = None,
        offset: int | None = None,
    ) -> None:
        self.message = message
        self.box_type = box_type
        self.offset = offset
        super().__init__(self.describe())

    def describe(self) -> str:
        if isinstance(self.box_type, bytes):
            label = self.box_type.decode("latin-1", "replace")
        else:
            label = self.box_type
        location = ""
        if label is not None:
            location += f"box {label}"
        if self.offset is not None:
            location += f" at offset {self.offset}"
        if location:
            return f"{location}: {self.message}"
        return self.message


class UsageError(MediidxError):
    """Bad command-line arguments or an unreadable/ unwritable local file."""

    exit_code = 1


class StructureError(MediidxError):
    """The input is not a supported, well-formed ISO BMFF file."""

    exit_code = 2


class ConsistencyError(MediidxError):
    """Two parts of the file disagree with each other."""

    exit_code = 2
