"""medcheck: offline media container metadata extractor and consistency checker.

Sniffs WAV (RIFF/RF64) and MP4/MOV (ISO BMFF) files by content, extracts
normalized track records, checks container-vs-track consistency, and stores
batch results in sqlite3 for reproducible review.
"""

__version__ = "0.1.0"

# Bump whenever the extraction logic changes; cached probe results keyed on
# an older parser version are discarded and files are re-extracted.
PARSER_VERSION = "1.0.0"

# Hard safety limits applied to every input file.
MAX_FILE_BYTES = 2 * 1024 ** 3  # 2 GiB
MAX_BOX_DEPTH = 16

__all__ = ["__version__", "PARSER_VERSION", "MAX_FILE_BYTES", "MAX_BOX_DEPTH"]
