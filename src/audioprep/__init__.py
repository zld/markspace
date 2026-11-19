"""audioprep: offline batch WAV preparation pipeline.

Modules:
    resample  - self-developed polyphase FIR resampling kernel
    layout    - interleaved frame layout, channel mapping, tail zero-padding
    loudness  - RMS / approximate K-weighted LUFS / true peak / gain staging
    wavio     - WAV read/write via soundfile with validation and atomic writes
    batch     - input discovery, task state and concurrent batch processing
    report    - JSON batch report assembly and atomic persistence
    cli       - argument parsing, process/verify subcommands, exit codes
"""

VERSION = "0.1.0"
__version__ = VERSION
__all__ = ["VERSION"]
