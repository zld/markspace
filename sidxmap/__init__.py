"""Offline ISO BMFF sidx/stbl index rebuild tool.

The package never decodes media. It parses box structure, expands sample
tables, validates byte ranges against mdat extents and emits byte-range
manifests suitable for chapter patchers.
"""

__all__ = ["model", "boxes", "stbl", "sidx", "validate", "rebuild", "fixture"]
__version__ = "1.0.0"
