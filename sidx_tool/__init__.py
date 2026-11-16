"""Local BMFF fragment-index rebuild tool.

Pure-stdlib Python package that parses classic (non-fragmented, unencrypted)
MP4 containers, validates sample tables, rebuilds segment boundaries and
serializes a fresh SIDX box.  No decoding, no network access.
"""

__all__ = ["errors"]
__version__ = "1.0.0"

# Hard safety limits (see spec: bounded nesting / sample counts).
MAX_BOX_DEPTH = 16
MAX_BOXES = 200_000
MAX_SAMPLES = 1_000_000
MAX_BOX_SIZE = 1 << 48  # 256 TiB, far beyond any real local file
IO_BUFFER = 1 << 16  # 64 KiB bounded copy buffer
