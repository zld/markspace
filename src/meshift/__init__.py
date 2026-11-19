"""meshift: a dependency-light Matroska -> MP4 remuxing pipeline.

The package implements its own EBML/Matroska parser and MP4 writer so that
compressed sample bytes are preserved while timestamps are rewritten under
explicit, verifiable rules.
"""

__version__ = "0.1.0"

#: Hard ceiling for a single remuxed output file (64 MiB).
MAX_OUTPUT_BYTES = 64 * 1024 * 1024
