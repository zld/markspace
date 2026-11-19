"""mkvmp4: a from-scratch Matroska -> MP4 stream-remuxing toolkit.

Only H.264/AVC video and AAC audio are supported. No transcoding and no
external muxing tools are used: EBML and ISO BMFF are read and written with
the Python standard library; NumPy is used for timestamp validation.
"""

__version__ = "0.1.0"

# Hard resource limits so corrupt inputs cannot trigger unbounded work.
MAX_ELEMENT_SIZE = 1 << 32          # a single EBML element: 4 GiB
MAX_TOTAL_INPUT_SIZE = 8 * (1 << 30)  # whole input file: 8 GiB
MAX_TOTAL_SAMPLES = 2_000_000
MAX_ELEMENT_DEPTH = 64
