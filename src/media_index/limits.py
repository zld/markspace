"""Hard resource limits used to keep malformed inputs controllable.

Every limit is deliberately conservative: a crafted file declaring huge
entry counts must fail fast instead of triggering unbounded allocation.
"""

# Maximum accepted file / view size (512 MiB). Anything larger is refused
# before parsing begins.
MAX_FILE_SIZE = 512 * 1024 * 1024

# Maximum nested box depth (moov/trak/mdia/minf/stbl ...).
MAX_BOX_DEPTH = 32

# Maximum number of sidx references (media + index) in a single sidx box.
MAX_SIDX_REFERENCES = 1_000_000

# Maximum sidx nesting depth when following index references.
MAX_SIDX_NESTING = 16

# Maximum number of entries for each stbl sample table.
MAX_TABLE_ENTRIES = 5_000_000

# Maximum number of chunks (stco/co64 entries).
MAX_CHUNKS = 1_000_000

# Tick accumulations are clamped to this many timescale ticks to detect
# integer overflow style runaway durations.
MAX_TICK_ACCUM = (1 << 62) - 1
